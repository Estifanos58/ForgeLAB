package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/detector"
)

// Helper to create tar archive containing specific files
func createTarArchiveWithFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// 1. Dockerfile containing RUN --mount=type=cache is recognized as requiring BuildKit.
// 2. Normal Dockerfile does NOT incorrectly require BuildKit.
// 3. Generated ForgeLAB Dockerfile does NOT incorrectly require BuildKit.
func TestRegression_DetectDockerfileRequiresBuildKit(t *testing.T) {
	t.Run("RUN --mount=type=cache is recognized as requiring BuildKit", func(t *testing.T) {
		df := []byte(`
FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /app/forgelab-server ./cmd/server
FROM alpine:3.19
COPY --from=builder /app/forgelab-server .
CMD ["./forgelab-server"]
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required, "RUN --mount=type=cache must require BuildKit")
		assert.Contains(t, feature, "RUN --mount=type=cache")
	})

	t.Run("Multiline RUN with backslash continuation and mount cache", func(t *testing.T) {
		df := []byte(`
FROM golang:alpine
RUN \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /app/server .
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "--mount=type=cache")
	})

	t.Run("RUN --mount=type=secret is recognized as requiring BuildKit", func(t *testing.T) {
		df := []byte(`
FROM alpine:3.19
RUN --mount=type=secret,id=mysecret cat /run/secrets/mysecret
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "--mount=type=secret")
	})

	t.Run("syntax directive requires BuildKit", func(t *testing.T) {
		df := []byte(`# syntax=docker/dockerfile:1.4
FROM alpine:3.19
RUN echo hello
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "syntax directive")
	})

	t.Run("COPY --link requires BuildKit", func(t *testing.T) {
		df := []byte(`FROM alpine:3.19
COPY --link src /app
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "COPY --link")
	})

	t.Run("COPY --chmod requires BuildKit", func(t *testing.T) {
		df := []byte(`FROM alpine:3.19
COPY --chmod=755 script.sh /app/script.sh
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "COPY --chmod")
	})

	t.Run("heredoc syntax requires BuildKit", func(t *testing.T) {
		df := []byte(`FROM alpine:3.19
RUN <<EOF
echo "hello world"
EOF
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.True(t, required)
		assert.Contains(t, feature, "heredoc syntax")
	})

	t.Run("Normal classic Dockerfile does NOT require BuildKit", func(t *testing.T) {
		df := []byte(`
FROM alpine:3.19
WORKDIR /app
COPY . .
RUN apk add --no-cache curl
EXPOSE 8080
CMD ["./server"]
`)
		required, feature := DetectDockerfileRequiresBuildKit(df)
		assert.False(t, required, "standard classic Dockerfile must not require BuildKit")
		assert.Empty(t, feature)
	})

	t.Run("Generated ForgeLAB Dockerfile does NOT incorrectly require BuildKit", func(t *testing.T) {
		runtimes := []string{"nodejs", "go", "python", "java", "rust"}
		for _, rt := range runtimes {
			content := detector.GenerateDockerfile(rt, 8080, "")
			required, feature := DetectDockerfileRequiresBuildKit([]byte(content))
			assert.False(t, required, "generated Dockerfile for %s must not incorrectly require BuildKit: %s", rt, feature)
		}
	})
}

// 4. Docker daemon reports Builder-Version: 2 → ForgeLAB selects BuildKit.
func TestRegression_ResolveBuilder_DaemonReportsBuilderVersion2_SelectsBuildKit(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping", "/v1.41/_ping":
			w.Header().Set("API-Version", "1.41")
			w.Header().Set("Builder-Version", "2") // BuildKit advertised
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	eng := &Engine{dockerClient: cli}

	var loggedLines []string
	logFn := func(msg string) {
		loggedLines = append(loggedLines, msg)
	}

	// Normal classic Dockerfile with no explicit options
	normalDF := []byte("FROM alpine:3.19\nCMD [\"echo\", \"hi\"]\n")
	resolvedOpts, err := eng.resolveBuilder(context.Background(), normalDF, types.ImageBuildOptions{}, logFn)
	require.NoError(t, err)

	// Since daemon reports Builder-Version: 2, ForgeLAB selects BuildKit
	assert.Equal(t, types.BuilderBuildKit, resolvedOpts.Version, "must select BuildKit when daemon reports Builder-Version: 2")

	// Verify logged diagnostics
	allLogs := strings.Join(loggedLines, "\n")
	assert.Contains(t, allLogs, "Docker daemon available")
	assert.Contains(t, allLogs, "Builder version: 2 / BuildKit")
	assert.Contains(t, allLogs, "Using BuildKit builder for Docker image build")
}

// 5. Docker daemon reports Builder-Version: 1 → ForgeLAB handles it correctly:
// - Normal Dockerfile: selects BuilderV1 (legacy), builds without error.
// - BuildKit-requiring Dockerfile: fails fast with clear diagnostic message.
func TestRegression_ResolveBuilder_DaemonReportsBuilderVersion1_HandlesCorrectly(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping", "/v1.41/_ping":
			w.Header().Set("API-Version", "1.41")
			w.Header().Set("Builder-Version", "1") // Legacy builder advertised
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	eng := &Engine{dockerClient: cli}

	t.Run("Normal Dockerfile on Builder-Version 1 uses legacy builder without error", func(t *testing.T) {
		var loggedLines []string
		logFn := func(msg string) { loggedLines = append(loggedLines, msg) }

		normalDF := []byte("FROM alpine:3.19\nCMD [\"echo\", \"hi\"]\n")
		resolvedOpts, err := eng.resolveBuilder(context.Background(), normalDF, types.ImageBuildOptions{}, logFn)
		require.NoError(t, err, "normal Dockerfile must not error on legacy daemon")

		assert.Equal(t, types.BuilderV1, resolvedOpts.Version, "must select legacy builder (v1) on Builder-Version: 1")

		allLogs := strings.Join(loggedLines, "\n")
		assert.Contains(t, allLogs, "Docker daemon available")
		assert.Contains(t, allLogs, "Builder version: 1 / legacy")
		assert.Contains(t, allLogs, "Using legacy builder for Docker image build")
	})

	t.Run("Dockerfile requiring BuildKit on Builder-Version 1 fails with clear diagnostic", func(t *testing.T) {
		var loggedLines []string
		logFn := func(msg string) { loggedLines = append(loggedLines, msg) }

		buildkitDF := []byte(`
FROM golang:alpine
RUN --mount=type=cache,target=/root/.cache/go-build go build ./cmd/server
`)
		_, err := eng.resolveBuilder(context.Background(), buildkitDF, types.ImageBuildOptions{}, logFn)
		require.Error(t, err, "must fail fast when BuildKit is required but daemon only supports Builder-Version: 1")

		// Verify clear diagnostic message
		assert.Contains(t, err.Error(), "BuildKit is required by Dockerfile feature RUN --mount=type=cache")
		assert.Contains(t, err.Error(), "but the configured Docker daemon does not support BuildKit")

		allLogs := strings.Join(loggedLines, "\n")
		assert.Contains(t, allLogs, "Docker daemon available")
		assert.Contains(t, allLogs, "Builder version: 1 / legacy")
		assert.Contains(t, allLogs, "BuildKit is required by Dockerfile feature RUN --mount=type=cache")
	})
}

// 6. ImageBuildOptions requests BuildKit when required during buildImage call.
func TestRegression_ImageBuildOptions_RequestsBuildKitWhenRequired(t *testing.T) {
	var requestedVersion atomic.Value
	requestedVersion.Store("")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.41")
			w.Header().Set("Builder-Version", "2")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case strings.Contains(r.URL.Path, "/build"):
			// Capture the 'version' query parameter sent by Docker SDK
			v := r.URL.Query().Get("version")
			requestedVersion.Store(v)

			w.WriteHeader(http.StatusOK)
			// Return successful build stream
			_, _ = fmt.Fprintln(w, `{"stream":"Step 1/2 : FROM golang:alpine\n"}`)
			_, _ = fmt.Fprintln(w, `{"stream":"Successfully built abc123def456\n"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	eng := &Engine{dockerClient: cli}

	// Tar archive containing Dockerfile with RUN --mount=type=cache
	backendDF := []byte(`
FROM golang:alpine
RUN --mount=type=cache,target=/root/.cache/go-build go build -o /app/server .
CMD ["/app/server"]
`)
	tarData := createTarArchiveWithFiles(t, map[string][]byte{
		"Dockerfile": backendDF,
		"main.go":    []byte("package main\nfunc main(){}"),
	})

	var logLines []string
	tarRC := io.NopCloser(bytes.NewReader(tarData))
	buildOpts := types.ImageBuildOptions{
		Tags:       []string{"forgelab/test-service:1"},
		Dockerfile: "Dockerfile",
	}

	buildErr := eng.buildImage(context.Background(), tarRC, buildOpts, func(msg string) {
		logLines = append(logLines, msg)
	})
	require.NoError(t, buildErr)

	// Verify Docker SDK ImageBuild was called with version=2 (BuildKit)
	assert.Equal(t, string(types.BuilderBuildKit), requestedVersion.Load().(string),
		"Docker ImageBuild must receive query param version=2 (BuildKit)")
}

// 7. Tar archive peeking preserves the complete stream for ImageBuild.
func TestRegression_TarPeeking_PreservesStreamIntact(t *testing.T) {
	files := map[string][]byte{
		"sub/file.txt": []byte("some asset file contents"),
		"Dockerfile":   []byte("FROM alpine\nRUN --mount=type=cache,target=/cache echo 1\n"),
		"README.md":    []byte("# Title"),
	}
	tarBytes := createTarArchiveWithFiles(t, files)

	originalRC := io.NopCloser(bytes.NewReader(tarBytes))
	wrappedRC, dfContent, err := peekDockerfileFromTar(originalRC, "Dockerfile")
	require.NoError(t, err)
	require.NotNil(t, wrappedRC)

	// Verify extracted Dockerfile content
	assert.Contains(t, string(dfContent), "RUN --mount=type=cache")

	// Read entire reconstructed stream and verify all original files exist
	reconstructedBytes, err := io.ReadAll(wrappedRC)
	require.NoError(t, err)
	require.Equal(t, len(tarBytes), len(reconstructedBytes), "reconstructed stream must have exact same byte length")

	tr := tar.NewReader(bytes.NewReader(reconstructedBytes))
	readFiles := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		readFiles[hdr.Name] = data
	}

	assert.Equal(t, len(files), len(readFiles))
	for name, expectedContent := range files {
		assert.Equal(t, expectedContent, readFiles[name], "file %s must match original contents", name)
	}
}

// 8. Shared migrate/backend build artifact logic coordinates builds and reuses image.
func TestRegression_SharedBuildArtifact_Coordination(t *testing.T) {
	eng := &Engine{}

	projectID := uuid.New()
	srcRev := "rev-123"
	cleanCtx := "./backend"
	cleanDF := "Dockerfile"

	sharedKey := fmt.Sprintf("%s:%s:%s:%s", projectID.String(), srcRev, cleanCtx, cleanDF)

	// First service (e.g. migrate) initializes the promise
	p1, loaded1 := eng.getOrInitSharedBuild(sharedKey)
	assert.False(t, loaded1, "first service must initialize the shared promise")
	assert.NotNil(t, p1)

	// Second service (e.g. backend) retrieves the existing promise
	p2, loaded2 := eng.getOrInitSharedBuild(sharedKey)
	assert.True(t, loaded2, "second service must load the existing shared promise")
	assert.Same(t, p1, p2, "both services must share the identical promise pointer")

	// Simulate first service finishing the build
	expectedImageTag := "forgelab/" + projectID.String() + "/backend:1"
	expectedDigest := "sha256:1234567890abcdef"
	p1.imageTag = expectedImageTag
	p1.digest = expectedDigest
	close(p1.done)

	// Verify second service sees the completed artifact
	select {
	case <-p2.done:
		assert.Equal(t, expectedImageTag, p2.imageTag)
		assert.Equal(t, expectedDigest, p2.digest)
		assert.NoError(t, p2.err)
	default:
		t.Fatal("shared promise done channel must be closed")
	}
}

func TestRegression_StreamBuildContext_PeekTar(t *testing.T) {
	backendDir := ""
	for _, candidate := range []string{".", "..", filepath.Join("..", ".."), filepath.Join("..", "backend"), "backend"} {
		if fi, err := os.Stat(filepath.Join(candidate, "Dockerfile")); err == nil && !fi.IsDir() {
			backendDir, _ = filepath.Abs(candidate)
			break
		}
	}
	if backendDir == "" {
		t.Skip("Skipping tar peeking test: Dockerfile not found")
	}

	matcher, err := LoadDockerignore(backendDir)
	if err != nil || matcher == nil {
		matcher = NewDockerignoreMatcher(DefaultIgnorePatterns)
	}

	tarArchive := StreamBuildContext(context.Background(), TarStreamerOptions{
		BuildContextDir: backendDir,
		Matcher:         matcher,
	})

	wrappedRC, dfContent, err := peekDockerfileFromTar(tarArchive, "Dockerfile")
	require.NoError(t, err)
	defer wrappedRC.Close()

	assert.NotEmpty(t, dfContent)
	t.Logf("Peeked Dockerfile length: %d", len(dfContent))

	tr := tar.NewReader(wrappedRC)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "reading tar entry %d failed: %v", count, err)
		_ = hdr
		count++
		_, _ = io.Copy(io.Discard, tr)
	}
	t.Logf("Successfully read %d tar entries", count)
}

func TestLive_BuildKit_BuildImage(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_LIVE_TEST") != "" {
		t.Skip("Skipping live Docker test")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker daemon not available: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Ping(context.Background()); err != nil {
		t.Skipf("Docker daemon ping failed: %v", err)
	}

	backendDir := ""
	for _, candidate := range []string{".", "..", filepath.Join("..", ".."), filepath.Join("..", "backend"), "backend"} {
		if fi, err := os.Stat(filepath.Join(candidate, "Dockerfile")); err == nil && !fi.IsDir() {
			backendDir, _ = filepath.Abs(candidate)
			break
		}
	}
	require.NotEmpty(t, backendDir, "backend directory containing Dockerfile must be found")

	matcher, _ := LoadDockerignore(backendDir)

	tarArchive := StreamBuildContext(context.Background(), TarStreamerOptions{
		BuildContextDir: backendDir,
		Matcher:         matcher,
	})

	opts := types.ImageBuildOptions{
		Tags:       []string{"forgelab-test-buildimage:latest"},
		Dockerfile: "Dockerfile",
	}

	eng := &Engine{dockerClient: cli}
	err = eng.buildImage(context.Background(), tarArchive, opts, func(s string) {
		t.Log(s)
	})
	require.NoError(t, err)
}
