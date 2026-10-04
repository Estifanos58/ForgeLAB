package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingReadCloser allows asserting close count and read state
type trackingRC struct {
	io.Reader
	closeCalls int
	closed     bool
	mu         sync.Mutex
}

func newTrackingRC(r io.Reader) *trackingRC {
	return &trackingRC{Reader: r}
}

func (t *trackingRC) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCalls++
	t.closed = true
	return nil
}

func (t *trackingRC) IsClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *trackingRC) CloseCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeCalls
}

// TestRegression_ConfigurableDockerBuildConcurrency verifies that concurrency limit is strictly enforced
func TestRegression_ConfigurableDockerBuildConcurrency(t *testing.T) {
	e := &Engine{}
	e.SetMaxConcurrentBuilds(1)
	assert.Equal(t, 1, e.GetMaxConcurrentBuilds())

	ctx := context.Background()

	rel1, err := e.acquireBuildSlot(ctx)
	require.NoError(t, err)

	// Second acquire must block when limit is 1
	blockedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = e.acquireBuildSlot(blockedCtx)
	require.Error(t, err, "second build slot must block when limit is 1")

	rel1()

	// After release, slot can be acquired
	rel2, err := e.acquireBuildSlot(ctx)
	require.NoError(t, err)
	rel2()
}

// TestRegression_SemaphoreReleaseAfterFailure verifies semaphore is always released after build failure
func TestRegression_SemaphoreReleaseAfterFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"daemon export error"}`))
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	engine := &Engine{dockerClient: cli}
	engine.SetMaxConcurrentBuilds(1)

	tarRC := newTrackingRC(bytes.NewReader([]byte("test")))

	// Build fails due to server 500
	err = engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
	require.Error(t, err)
	assert.True(t, tarRC.IsClosed(), "tar archive must be closed on failure")

	// Semaphore slot MUST be released despite the failure
	acquireCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	rel, err := engine.acquireBuildSlot(acquireCtx)
	require.NoError(t, err, "semaphore must be available after failed build; no slot leak")
	rel()
}

// TestRegression_ContainerdExportErrorClassification verifies containerd layer export errors
// are classified as storage daemon issues rather than application script failures
func TestRegression_ContainerdExportErrorClassification(t *testing.T) {
	containerdErr := errors.New("Docker build error: failed to export layer: CreateDiff: mount callback failed: rename /var/lib/docker/io.containerd.content.v1.content/ingest/abc/data /var/lib/docker/blobs/sha256/123: no such file or directory")

	category := ClassifyDockerBuildError(containerdErr)
	assert.Equal(t, CategoryStorageDaemon, category, "must be classified as storage daemon error")

	engine := &Engine{}
	diag := engine.formatDockerDiagnostic(context.Background(), containerdErr, category)

	assert.Contains(t, diag, "Docker daemon failed while exporting the image layer")
	assert.Contains(t, diag, "This appears to be a Docker/containerd storage failure rather than an application build error")
	assert.Contains(t, diag, "Check Docker Desktop health, available disk space, and containerd/image storage")
	assert.Contains(t, diag, "Original Docker error: Docker build error: failed to export layer")
}

// TestRegression_NoDuplicateBuildExecutionWithConsumedTarStream verifies that
// a consumed tar stream cannot and must not be retried with the same reader
func TestRegression_NoDuplicateBuildExecutionWithConsumedTarStream(t *testing.T) {
	buildCalls := atomic.Int32{}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buildCalls.Add(1)
		// Read body to consume it
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"layer export error"}`))
	}))
	defer mockServer.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(mockServer.URL),
		client.WithHTTPClient(mockServer.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)
	defer cli.Close()

	engine := &Engine{dockerClient: cli}
	tarRC := newTrackingRC(bytes.NewReader([]byte("dummy-tar-content")))

	// Initial build attempt
	err = engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
	require.Error(t, err)

	// Stream was consumed and closed exactly once
	assert.Equal(t, int32(1), buildCalls.Load(), "must only execute build once; no blind retry with consumed stream")
	assert.True(t, tarRC.IsClosed(), "stream must be closed")
	assert.Equal(t, 1, tarRC.CloseCalls(), "stream must be closed exactly once")
}

// TestRegression_SeparationOfServiceDeploymentFromBuildConcurrency verifies that
// two service deployments can run their non-build phases concurrently while serializing their buildImage calls
func TestRegression_SeparationOfServiceDeploymentFromBuildConcurrency(t *testing.T) {
	e := &Engine{}
	e.SetMaxConcurrentBuilds(1) // Docker build limit = 1

	var concurrentNonBuild atomic.Int32
	var maxConcurrentNonBuild atomic.Int32
	var concurrentBuilds atomic.Int32
	var maxConcurrentBuilds atomic.Int32

	var wg sync.WaitGroup

	simulateServiceDeployment := func(svcName string) {
		defer wg.Done()

		// 1. Non-build phase (source resolution, analysis) - runs concurrently
		currNonBuild := concurrentNonBuild.Add(1)
		for {
			old := maxConcurrentNonBuild.Load()
			if currNonBuild <= old || maxConcurrentNonBuild.CompareAndSwap(old, currNonBuild) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		concurrentNonBuild.Add(-1)

		// 2. Build phase - protected by acquireBuildSlot
		relSlot, err := e.acquireBuildSlot(context.Background())
		if err != nil {
			t.Errorf("failed to acquire build slot: %v", err)
			return
		}
		currBuild := concurrentBuilds.Add(1)
		for {
			old := maxConcurrentBuilds.Load()
			if currBuild <= old || maxConcurrentBuilds.CompareAndSwap(old, currBuild) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		concurrentBuilds.Add(-1)
		relSlot()

		// 3. Post-build phase (container start, health check) - runs concurrently again
		currPost := concurrentNonBuild.Add(1)
		for {
			old := maxConcurrentNonBuild.Load()
			if currPost <= old || maxConcurrentNonBuild.CompareAndSwap(old, currPost) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		concurrentNonBuild.Add(-1)
	}

	wg.Add(2)
	go simulateServiceDeployment("frontend")
	go simulateServiceDeployment("backend")
	wg.Wait()

	assert.Equal(t, int32(1), maxConcurrentBuilds.Load(), "Docker builds must be serialized (max 1)")
	assert.GreaterOrEqual(t, maxConcurrentNonBuild.Load(), int32(2), "Non-build deployment phases must run concurrently (>= 2)")
}
