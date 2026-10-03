package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
)

// trackingReadCloser tracks whether and when a stream is read and closed.
type trackingReadCloser struct {
	reader     io.Reader
	closeCalls int32
	closed     atomic.Bool
	closedAt   time.Time
	mu         sync.Mutex
}

func newTrackingReadCloser(r io.Reader) *trackingReadCloser {
	return &trackingReadCloser{reader: r}
}

func (t *trackingReadCloser) Read(p []byte) (int, error) {
	if t.closed.Load() {
		return 0, fmt.Errorf("read called on closed ReadCloser")
	}
	return t.reader.Read(p)
}

func (t *trackingReadCloser) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	atomic.AddInt32(&t.closeCalls, 1)
	t.closed.Store(true)
	t.closedAt = time.Now()
	if c, ok := t.reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (t *trackingReadCloser) IsClosed() bool {
	return t.closed.Load()
}

func (t *trackingReadCloser) CloseCalls() int {
	return int(atomic.LoadInt32(&t.closeCalls))
}

// createSampleTarArchive returns a minimal valid tar archive.
func createSampleTarArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dockerfile := []byte("FROM alpine:3.18\nCMD [\"echo\", \"ok\"]\n")
	hdr := &tar.Header{
		Name: "Dockerfile",
		Mode: 0644,
		Size: int64(len(dockerfile)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err := tw.Write(dockerfile)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	return buf.Bytes()
}

// TestEngine_BuildImage_ContextRemainsActiveDuringStreamParsing proves that a successful
// ImageBuild() response can be fully consumed before the build context is cancelled,
// and that 'context canceled' is not produced simply because the response parser starts after ImageBuild().
func TestEngine_BuildImage_ContextRemainsActiveDuringStreamParsing(t *testing.T) {
	var (
		serverReceivedBuild = atomic.Bool{}
		serverFinishedSend  = atomic.Bool{}
		streamLinesReceived []string
		streamMu            sync.Mutex
	)

	// Mock Docker daemon HTTP server
	mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" || r.URL.Path == "/v1.41/_ping" {
			w.Header().Set("API-Version", "1.41")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}

		if r.Method == http.MethodPost && (r.URL.Path == "/v1.41/build" || r.URL.Path == "/build") {
			serverReceivedBuild.Store(true)

			// Fully consume the tar archive request body
			_, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			messages := []string{
				`{"stream":"Step 1/3 : FROM alpine:3.18\n"}`,
				`{"stream":"Step 2/3 : RUN echo building ccms-backend\n"}`,
				`{"stream":"Step 3/3 : CMD [\"node\", \"server.js\"]\n"}`,
				`{"stream":"Successfully built 9a8b7c6d5e4f\n"}`,
				`{"stream":"Successfully tagged forgelab/test/ccms-backend:1\n"}`,
				`{"aux":{"ID":"sha256:9a8b7c6d5e4f"}}`,
			}

			for _, msg := range messages {
				// Simulate realistic build progress streaming over time
				time.Sleep(15 * time.Millisecond)
				_, _ = fmt.Fprintf(w, "%s\n", msg)
				if ok {
					flusher.Flush()
				}
			}

			serverFinishedSend.Store(true)
			return
		}

		http.NotFound(w, r)
	}))
	defer mockDockerServer.Close()

	dockerCli, err := client.NewClientWithOpts(
		client.WithHost(mockDockerServer.URL),
		client.WithHTTPClient(mockDockerServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer dockerCli.Close()

	engine := NewEngine(
		dockerCli,
		nil,
		nil,
		nil,
		nil,
		nil,
		network.NewPortManager(10000, 20000),
		security.NewPathValidator([]string{"."}),
		nil,
		t.TempDir(),
	)

	tarData := createSampleTarArchive(t)
	tarRC := newTrackingReadCloser(bytes.NewReader(tarData))

	buildOpts := types.ImageBuildOptions{
		Tags:       []string{"forgelab/test/ccms-backend:1"},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	buildStartTime := time.Now()
	buildErr := engine.buildImage(context.Background(), tarRC, buildOpts, func(line string) {
		streamMu.Lock()
		streamLinesReceived = append(streamLinesReceived, line)
		streamMu.Unlock()
	})

	// Assertions
	require.NoError(t, buildErr, "buildImage must not return an error (such as context canceled)")
	assert.True(t, serverReceivedBuild.Load(), "Docker daemon should have received the build request")
	assert.True(t, serverFinishedSend.Load(), "Docker daemon should have completed sending build stream")

	streamMu.Lock()
	defer streamMu.Unlock()

	require.Len(t, streamLinesReceived, 5, "all stream log lines should have been parsed without cancellation interruption")
	assert.Contains(t, streamLinesReceived[0], "Step 1/3")
	assert.Contains(t, streamLinesReceived[1], "building ccms-backend")
	assert.Contains(t, streamLinesReceived[3], "Successfully built 9a8b7c6d5e4f")

	// Verify tarArchive was closed exactly once and only after build finished
	assert.True(t, tarRC.IsClosed(), "source tarArchive must be closed after build completes")
	assert.Equal(t, 1, tarRC.CloseCalls(), "source tarArchive must be closed exactly once")
	assert.True(t, tarRC.closedAt.After(buildStartTime), "tarArchive close time must be after build start")
}

// TestEngine_BuildImage_BuggyLifecycle_DemonstratesContextCanceled confirms the root cause:
// Cancelling the build context immediately after ImageBuild() causes parseDockerStream
// to fail with 'context canceled'.
func TestEngine_BuildImage_BuggyLifecycle_DemonstratesContextCanceled(t *testing.T) {
	mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		for i := 1; i <= 5; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
				_, err := fmt.Fprintf(w, `{"stream":"Step %d/5 : compiling\n"}`+"\n", i)
				if err != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}))
	defer mockDockerServer.Close()

	dockerCli, err := client.NewClientWithOpts(
		client.WithHost(mockDockerServer.URL),
		client.WithHTTPClient(mockDockerServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer dockerCli.Close()

	tarData := createSampleTarArchive(t)

	// Simulate the PREVIOUS BUGGY LIFECYCLE:
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 15*time.Minute)
	buildResponse, err := dockerCli.ImageBuild(buildCtx, bytes.NewReader(tarData), types.ImageBuildOptions{
		Tags:       []string{"test:buggy"},
		Dockerfile: "Dockerfile",
	})
	require.NoError(t, err)

	// Premature cancellation:
	buildCancel()

	engine := &Engine{}
	streamErr := engine.parseDockerStream(buildResponse.Body, func(line string) {})
	buildResponse.Body.Close()

	// Proves that premature cancellation produces 'context canceled'
	require.Error(t, streamErr, "reading stream after premature context cancellation must produce error")
	assert.Contains(t, streamErr.Error(), context.Canceled.Error(), "error must be 'context canceled'")
}

// TestEngine_BuildImage_LocalAgentStreamedTarArchive_RemainsOpenDuringBuildRequest verifies
// that the local-agent streamed tar archive remains open for the entire Docker build request
// and is not prematurely closed.
func TestEngine_BuildImage_LocalAgentStreamedTarArchive_RemainsOpenDuringBuildRequest(t *testing.T) {
	tarData := createSampleTarArchive(t)
	tarTracking := newTrackingReadCloser(bytes.NewReader(tarData))

	var (
		serverReadWhileOpen = atomic.Bool{}
		serverReadBytes     = atomic.Int64{}
	)

	mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/v1.41/build" || r.URL.Path == "/build") {
			// Read the streamed tar context in small chunks to verify the stream is kept open
			buf := make([]byte, 64)
			for {
				n, err := r.Body.Read(buf)
				if n > 0 {
					serverReadBytes.Add(int64(n))
					// Verify tar stream was NOT prematurely closed while server is reading
					if !tarTracking.IsClosed() {
						serverReadWhileOpen.Store(true)
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"stream":"Successfully built 123456\n"}` + "\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer mockDockerServer.Close()

	dockerCli, err := client.NewClientWithOpts(
		client.WithHost(mockDockerServer.URL),
		client.WithHTTPClient(mockDockerServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer dockerCli.Close()

	engine := NewEngine(
		dockerCli,
		nil,
		nil,
		nil,
		nil,
		nil,
		network.NewPortManager(10000, 20000),
		security.NewPathValidator([]string{"."}),
		nil,
		t.TempDir(),
	)

	// Verify tar archive is initially OPEN
	assert.False(t, tarTracking.IsClosed(), "tar archive must be open initially")

	var logs []string
	buildErr := engine.buildImage(context.Background(), tarTracking, types.ImageBuildOptions{
		Tags:       []string{"forgelab/agent-test:1"},
		Dockerfile: "Dockerfile",
	}, func(line string) {
		logs = append(logs, line)
	})

	require.NoError(t, buildErr)
	assert.True(t, serverReadWhileOpen.Load(), "mock Docker daemon must read while tar stream was open")
	assert.Equal(t, int64(len(tarData)), serverReadBytes.Load(), "mock Docker daemon must have received full tar archive")
	assert.True(t, tarTracking.IsClosed(), "tar archive must be closed after build completes")
	assert.Equal(t, 1, tarTracking.CloseCalls(), "tar archive must be closed exactly once")
	assert.Contains(t, logs, "Successfully built 123456")
}

// TestEngine_BuildImage_StructuredCleanup_OnError verifies that defer/structured cleanup
// closes both the response body and the tar archive exactly once when build errors occur.
func TestEngine_BuildImage_StructuredCleanup_OnError(t *testing.T) {
	t.Run("ImageBuild failure closes tar archive and cancels context", func(t *testing.T) {
		mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"daemon unavailable"}`))
		}))
		defer mockDockerServer.Close()

		dockerCli, err := client.NewClientWithOpts(
			client.WithHost(mockDockerServer.URL),
			client.WithHTTPClient(mockDockerServer.Client()),
			client.WithVersion("1.41"),
		)
		require.NoError(t, err)
		defer dockerCli.Close()

		engine := &Engine{dockerClient: dockerCli}
		tarRC := newTrackingReadCloser(bytes.NewReader([]byte("test")))

		err = engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "Docker build failed:")
		assert.True(t, tarRC.IsClosed(), "tarArchive must be closed on ImageBuild error")
		assert.Equal(t, 1, tarRC.CloseCalls(), "tarArchive must be closed exactly once")
	})

	t.Run("parseDockerStream error closes response body and tar archive", func(t *testing.T) {
		mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"error":"Dockerfile parse error: unknown instruction FOO"}` + "\n")
		}))
		defer mockDockerServer.Close()

		dockerCli, err := client.NewClientWithOpts(
			client.WithHost(mockDockerServer.URL),
			client.WithHTTPClient(mockDockerServer.Client()),
			client.WithVersion("1.41"),
		)
		require.NoError(t, err)
		defer dockerCli.Close()

		engine := &Engine{dockerClient: dockerCli}
		tarRC := newTrackingReadCloser(bytes.NewReader(createSampleTarArchive(t)))

		err = engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "Docker build error:")
		assert.Contains(t, err.Error(), "Dockerfile parse error")
		assert.True(t, tarRC.IsClosed(), "tarArchive must be closed on build stream error")
		assert.Equal(t, 1, tarRC.CloseCalls(), "tarArchive must be closed exactly once")
	})
}

// TestEngine_ExecuteServiceDeployment_LocalAgent_BuildLifecycle tests that ExecuteServiceDeployment
// successfully coordinates with a local agent source stream, builds the Docker image with active context,
// and properly consumes all build logs without 'context canceled'.
func TestEngine_ExecuteServiceDeployment_LocalAgent_BuildLifecycle(t *testing.T) {
	// 1. Mock Local Agent /stream-context server
	tarData := createSampleTarArchive(t)
	agentTarRC := newTrackingReadCloser(bytes.NewReader(tarData))

	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/sources/test-source-123/stream-context" {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-agent-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, agentTarRC)
	}))
	defer agentServer.Close()

	t.Setenv("FORGELAB_AGENT_URL", agentServer.URL)

	// 2. Mock Docker Daemon server
	var (
		receivedImageBuild = atomic.Bool{}
		buildLogsEmitted   []string
		logsMu             sync.Mutex
	)

	mockDockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1.41/_ping" || r.URL.Path == "/_ping":
			w.Header().Set("API-Version", "1.41")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case r.Method == http.MethodPost && (r.URL.Path == "/v1.41/build" || r.URL.Path == "/build"):
			receivedImageBuild.Store(true)
			// Read the body streamed from agent through Docker client
			_, _ = io.ReadAll(r.Body)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, `{"stream":"Building ccms-backend in ForgeLAB\n"}` + "\n")
			if ok {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
			_, _ = fmt.Fprintf(w, `{"stream":"Successfully built feedbeef1234\n"}` + "\n")
			if ok {
				flusher.Flush()
			}
			_, _ = fmt.Fprintf(w, `{"aux":{"ID":"sha256:feedbeef1234"}}` + "\n")
		case r.Method == http.MethodGet && (r.URL.Path == "/v1.41/images/json" || len(r.URL.Path) > 15 && r.URL.Path[:15] == "/v1.41/images/"):
			// ImageInspectWithRaw
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"Id":"sha256:feedbeef1234","RepoDigests":["forgelab/test:feedbeef1234"]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1.41/networks/forgelab-net-":
			w.WriteHeader(http.StatusOK)
		default:
			// For container creation, we can return 500 so deployment stops gracefully at startup
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"stopping test before container startup"}`))
		}
	}))
	defer mockDockerServer.Close()

	dockerCli, err := client.NewClientWithOpts(
		client.WithHost(mockDockerServer.URL),
		client.WithHTTPClient(mockDockerServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer dockerCli.Close()

	// 3. Setup SourceService with encrypted agent token
	cryptoKey := "12345678901234567890123456789012"
	encryptor, err := crypto.NewEncryptor(cryptoKey)
	require.NoError(t, err)

	sourceService := services.NewSourceService(nil, t.TempDir(), encryptor)
	ownerID := uuid.New()
	sourceUUID, err := uuid.Parse("00000000-0000-0000-0000-000000000123")
	require.NoError(t, err)

	encryptedToken, err := sourceService.EncryptToken("valid-agent-token")
	require.NoError(t, err)

	err = sourceService.SaveSource(context.Background(), &models.Source{
		ID:                    sourceUUID,
		OwnerID:               ownerID,
		SourceType:            models.SourceTypeLocalAgent,
		SourceReference:       "test-source-123",
		EncryptedSessionToken: encryptedToken,
	})
	require.NoError(t, err)

	// 4. Test engine.buildImage with the agent stream
	// Note: We test the build phase of ExecuteServiceDeployment directly
	engine := NewEngine(
		dockerCli,
		nil,
		nil,
		nil,
		sourceService,
		nil,
		network.NewPortManager(10000, 20000),
		security.NewPathValidator([]string{"."}),
		nil,
		t.TempDir(),
	)

	// Fetch stream from local agent like ExecuteServiceDeployment does
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		agentServer.URL+"/api/agent/sources/test-source-123/stream-context?service_path=.&runtime=nodejs&port=8080&start_cmd=npm+start",
		nil,
	)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer valid-agent-token")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	clientTarRC := newTrackingReadCloser(resp.Body)

	buildOpts := types.ImageBuildOptions{
		Tags:       []string{"forgelab/test/ccms-backend:1"},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	buildErr := engine.buildImage(context.Background(), clientTarRC, buildOpts, func(msg string) {
		logsMu.Lock()
		buildLogsEmitted = append(buildLogsEmitted, msg)
		logsMu.Unlock()
	})

	require.NoError(t, buildErr, "Docker build of local-agent source must succeed without 'context canceled'")
	assert.True(t, receivedImageBuild.Load(), "Docker daemon must have received the build request")
	assert.True(t, clientTarRC.IsClosed(), "local agent tar stream response body must be closed after build completes")
	assert.Equal(t, 1, clientTarRC.CloseCalls(), "local agent tar stream must be closed exactly once")

	logsMu.Lock()
	defer logsMu.Unlock()
	assert.Contains(t, buildLogsEmitted, "Building ccms-backend in ForgeLAB")
	assert.Contains(t, buildLogsEmitted, "Successfully built feedbeef1234")
}
