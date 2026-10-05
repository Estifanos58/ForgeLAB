package docker

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a test Docker client wired to an httptest.Server
func newTestDockerClient(t *testing.T, handler http.Handler) (*client.Client, func()) {
	server := httptest.NewServer(handler)
	cli, err := client.NewClientWithOpts(
		client.WithHost(server.URL),
		client.WithHTTPClient(server.Client()),
		client.WithVersion("1.41"),
	)
	require.NoError(t, err)

	return cli, func() {
		_ = cli.Close()
		server.Close()
	}
}

// TestRegression_BuildPruneSync_TestA_PruneWaitsForActiveBuild verifies that
// Docker image pruning blocks and NEVER invokes ImagesPrune while an ImageBuild is active.
func TestRegression_BuildPruneSync_TestA_PruneWaitsForActiveBuild(t *testing.T) {
	buildInFlight := make(chan struct{})
	unblockBuild := make(chan struct{})
	var pruneCalls atomic.Int32
	var buildCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/build"):
			buildCalls.Add(1)
			close(buildInFlight)
			<-unblockBuild
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stream":"Step 1/1 : FROM alpine\n"}` + "\n"))
			_, _ = w.Write([]byte(`{"stream":"Successfully built test-img\n"}` + "\n"))
		case strings.Contains(r.URL.Path, "/images/prune"):
			pruneCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ImagesDeleted":[{"Deleted":"sha256:111"}],"SpaceReclaimed":500}` + "\n"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	engine := &Engine{dockerClient: cli}
	engine.SetMaxConcurrentBuilds(1)

	// 1. Start buildImage in background
	buildDone := make(chan error, 1)
	go func() {
		tarRC := newTrackingRC(bytes.NewReader([]byte("dummy-tar")))
		err := engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
		buildDone <- err
	}()

	// 2. Wait until build is actively inside the Docker build step
	<-buildInFlight
	assert.Equal(t, int32(1), buildCalls.Load())

	// 3. Trigger PruneDanglingResources in background while build is active
	pruneDone := make(chan error, 1)
	pruneStarted := make(chan struct{})
	go func() {
		close(pruneStarted)
		err := engine.PruneDanglingResources(context.Background(), 2*time.Hour)
		pruneDone <- err
	}()
	<-pruneStarted

	// Give goroutine a moment to hit the lock and verify ImagesPrune is NOT called
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), pruneCalls.Load(), "ImagesPrune must not be called while build holds shared lock")

	select {
	case <-pruneDone:
		t.Fatal("PruneDanglingResources must be blocked waiting for active build")
	default:
		// Expected: prune is blocked
	}

	// 4. Release active build
	close(unblockBuild)

	// 5. Verify build finishes successfully
	require.NoError(t, <-buildDone, "buildImage must complete cleanly")

	// 6. Verify prune unblocks and completes after build finishes
	select {
	case err := <-pruneDone:
		require.NoError(t, err, "PruneDanglingResources must complete without error")
	case <-time.After(2 * time.Second):
		t.Fatal("PruneDanglingResources did not complete within expected timeout after build finished")
	}

	assert.Equal(t, int32(1), pruneCalls.Load(), "ImagesPrune must be executed exactly once after build releases lock")
}

// TestRegression_BuildPruneSync_TestB_BuildWaitsForActivePrune verifies that
// Docker image builds block and NEVER start while ImagesPrune holds the exclusive lock.
func TestRegression_BuildPruneSync_TestB_BuildWaitsForActivePrune(t *testing.T) {
	pruneInFlight := make(chan struct{})
	unblockPrune := make(chan struct{})
	var pruneCalls atomic.Int32
	var buildCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/prune"):
			pruneCalls.Add(1)
			close(pruneInFlight)
			<-unblockPrune
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ImagesDeleted":[{"Deleted":"sha256:222"}],"SpaceReclaimed":1000}` + "\n"))
		case strings.Contains(r.URL.Path, "/build"):
			buildCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stream":"Successfully built test-img\n"}` + "\n"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	engine := &Engine{dockerClient: cli}
	engine.SetMaxConcurrentBuilds(1)

	// 1. Start PruneDanglingResources in background
	pruneDone := make(chan error, 1)
	go func() {
		err := engine.PruneDanglingResources(context.Background(), 2*time.Hour)
		pruneDone <- err
	}()

	// 2. Wait until prune is in-flight holding exclusive lock
	<-pruneInFlight
	assert.Equal(t, int32(1), pruneCalls.Load())

	// 3. Start buildImage in background while prune is active
	buildDone := make(chan error, 1)
	buildStarted := make(chan struct{})
	go func() {
		close(buildStarted)
		tarRC := newTrackingRC(bytes.NewReader([]byte("dummy-tar")))
		err := engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
		buildDone <- err
	}()
	<-buildStarted

	// Give build goroutine a moment to attempt acquiring shared lock
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), buildCalls.Load(), "ImageBuild must not be called while prune holds exclusive lock")

	select {
	case <-buildDone:
		t.Fatal("buildImage must be blocked waiting for active prune")
	default:
		// Expected: build is blocked
	}

	// 4. Release active prune
	close(unblockPrune)

	// 5. Verify prune finishes cleanly
	require.NoError(t, <-pruneDone, "PruneDanglingResources must complete successfully")

	// 6. Verify build proceeds and finishes cleanly
	select {
	case err := <-buildDone:
		require.NoError(t, err, "buildImage must complete without error after prune finishes")
	case <-time.After(2 * time.Second):
		t.Fatal("buildImage did not complete within expected timeout after prune finished")
	}

	assert.Equal(t, int32(1), buildCalls.Load(), "ImageBuild must be invoked after prune finishes")
}

// TestRegression_BuildPruneSync_TestC_MultipleBuildsCompatibility verifies that
// BuildSemaphore concurrency behavior is preserved and multiple builds can run concurrently
// under configured concurrency limits while blocking prune.
func TestRegression_BuildPruneSync_TestC_MultipleBuildsCompatibility(t *testing.T) {
	t.Run("concurrency 1: builds serialized, prune blocked behind active builds", func(t *testing.T) {
		var buildNum atomic.Int32
		var activeBuilds atomic.Int32
		var maxActiveBuilds atomic.Int32
		var pruneCalls atomic.Int32

		b1Started := make(chan struct{})
		unblockB1 := make(chan struct{})
		b2Started := make(chan struct{})
		unblockB2 := make(chan struct{})

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/build"):
				num := buildNum.Add(1)
				cur := activeBuilds.Add(1)
				for {
					old := maxActiveBuilds.Load()
					if cur <= old || maxActiveBuilds.CompareAndSwap(old, cur) {
						break
					}
				}

				if num == 1 {
					close(b1Started)
					<-unblockB1
				} else {
					close(b2Started)
					<-unblockB2
				}
				activeBuilds.Add(-1)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"stream":"Successfully built\n"}` + "\n"))
			case strings.Contains(r.URL.Path, "/images/prune"):
				pruneCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":0}` + "\n"))
			default:
				w.WriteHeader(http.StatusOK)
			}
		})

		cli, cleanup := newTestDockerClient(t, handler)
		defer cleanup()

		engine := &Engine{dockerClient: cli}
		engine.SetMaxConcurrentBuilds(1)

		b1Done := make(chan error, 1)
		go func() {
			tarRC := newTrackingRC(bytes.NewReader([]byte("tar1")))
			b1Done <- engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
		}()
		<-b1Started

		// Build 2 starts while Build 1 is in-flight: acquires RLock, then waits on semaphore
		b2Done := make(chan error, 1)
		go func() {
			tarRC := newTrackingRC(bytes.NewReader([]byte("tar2")))
			b2Done <- engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
		}()

		// Wait deterministically until Build 2 has acquired RLock and is queued on BuildSemaphore
		for engine.GetBuildWaitersCount() == 0 {
			time.Sleep(2 * time.Millisecond)
		}

		// Prune starts: should wait on maintenance lock behind active builds
		pruneDone := make(chan error, 1)
		go func() {
			pruneDone <- engine.PruneDanglingResources(context.Background(), 2*time.Hour)
		}()

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, int32(0), pruneCalls.Load(), "prune must not run while builds are in-flight")

		// Unblock Build 1
		close(unblockB1)
		require.NoError(t, <-b1Done)

		// Build 2 now enters build handler
		<-b2Started
		assert.Equal(t, int32(0), pruneCalls.Load(), "prune still blocked while Build 2 executes")

		// Unblock Build 2
		close(unblockB2)
		require.NoError(t, <-b2Done)

		// Prune now unblocks and finishes
		require.NoError(t, <-pruneDone)
		assert.Equal(t, int32(1), pruneCalls.Load())
		assert.Equal(t, int32(1), maxActiveBuilds.Load(), "max concurrent builds was 1 as configured")
	})

	t.Run("concurrency 2: multiple concurrent builds allowed while prune is blocked", func(t *testing.T) {
		var activeBuilds atomic.Int32
		var maxActiveBuilds atomic.Int32
		var pruneCalls atomic.Int32

		var bothBuildsRunning sync.WaitGroup
		bothBuildsRunning.Add(2)
		unblockAll := make(chan struct{})

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/build"):
				cur := activeBuilds.Add(1)
				for {
					old := maxActiveBuilds.Load()
					if cur <= old || maxActiveBuilds.CompareAndSwap(old, cur) {
						break
					}
				}
				bothBuildsRunning.Done()
				<-unblockAll
				activeBuilds.Add(-1)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"stream":"Successfully built\n"}` + "\n"))
			case strings.Contains(r.URL.Path, "/images/prune"):
				pruneCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":0}` + "\n"))
			default:
				w.WriteHeader(http.StatusOK)
			}
		})

		cli, cleanup := newTestDockerClient(t, handler)
		defer cleanup()

		engine := &Engine{dockerClient: cli}
		engine.SetMaxConcurrentBuilds(2) // Allow 2 concurrent builds

		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				tarRC := newTrackingRC(bytes.NewReader([]byte("tar")))
				err := engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
				assert.NoError(t, err)
			}()
		}

		// Wait until BOTH builds are running concurrently
		bothBuildsRunning.Wait()
		assert.Equal(t, int32(2), maxActiveBuilds.Load(), "both builds must be actively building concurrently")

		// Prune attempted while both are running
		pruneDone := make(chan error, 1)
		go func() {
			pruneDone <- engine.PruneDanglingResources(context.Background(), 2*time.Hour)
		}()

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, int32(0), pruneCalls.Load(), "prune must be blocked while 2 builds are running")

		// Unblock builds
		close(unblockAll)
		wg.Wait()

		// Prune now finishes
		require.NoError(t, <-pruneDone)
		assert.Equal(t, int32(1), pruneCalls.Load(), "prune executes after both builds finish")
	})
}

// TestRegression_BuildPruneSync_TestD_LongRunningBuildSchedulerSafety verifies that
// when a build runs longer than the maintenance scheduler interval, multiple maintenance
// triggers cannot invoke ImagesPrune during the in-flight build.
func TestRegression_BuildPruneSync_TestD_LongRunningBuildSchedulerSafety(t *testing.T) {
	buildInFlight := make(chan struct{})
	unblockBuild := make(chan struct{})
	var pruneCalls atomic.Int32
	var buildCalls atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/build"):
			buildCalls.Add(1)
			close(buildInFlight)
			<-unblockBuild
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stream":"Step 5/5 : npm ci\n"}` + "\n"))
			_, _ = w.Write([]byte(`{"stream":"Successfully built\n"}` + "\n"))
		case strings.Contains(r.URL.Path, "/images/prune"):
			pruneCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ImagesDeleted":[],"SpaceReclaimed":0}` + "\n"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	engine := &Engine{dockerClient: cli}
	engine.SetMaxConcurrentBuilds(1)

	// 1. Long-running build starts (simulating e.g. 4-minute npm ci self-deployment)
	buildDone := make(chan error, 1)
	go func() {
		tarRC := newTrackingRC(bytes.NewReader([]byte("dummy-context")))
		buildDone <- engine.buildImage(context.Background(), tarRC, types.ImageBuildOptions{}, func(s string) {})
	}()

	<-buildInFlight
	assert.Equal(t, int32(1), buildCalls.Load())

	// 2. Simulate 3 consecutive maintenance ticks firing while build is still active
	pruneFinishedCount := atomic.Int32{}
	var pruneWg sync.WaitGroup
	for i := 0; i < 3; i++ {
		pruneWg.Add(1)
		go func() {
			defer pruneWg.Done()
			err := engine.PruneDanglingResources(context.Background(), 2*time.Hour)
			assert.NoError(t, err)
			pruneFinishedCount.Add(1)
		}()
	}

	// Verify all prune attempts are blocked behind the in-flight build
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), pruneCalls.Load(), "no maintenance invocation may call ImagesPrune during build")
	assert.Equal(t, int32(0), pruneFinishedCount.Load(), "all maintenance routines must wait")

	// 3. Complete the long-running build
	close(unblockBuild)
	require.NoError(t, <-buildDone, "long-running build must complete successfully")

	// 4. Wait for queued maintenance cycles to finish
	pruneWg.Wait()
	assert.GreaterOrEqual(t, pruneCalls.Load(), int32(1), "maintenance proceeds once build completes")
}

// TestRegression_BuildPruneSync_PruneErrorPropagation verifies that ImagesPrune errors
// are not silently swallowed and are returned by PruneDanglingResources.
func TestRegression_BuildPruneSync_PruneErrorPropagation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/images/prune") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"docker daemon prune failed"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cli, cleanup := newTestDockerClient(t, handler)
	defer cleanup()

	engine := &Engine{dockerClient: cli}
	err := engine.PruneDanglingResources(context.Background(), 2*time.Hour)
	require.Error(t, err, "PruneDanglingResources must return error on ImagesPrune failure")
	assert.Contains(t, err.Error(), "docker image prune failed")
}
