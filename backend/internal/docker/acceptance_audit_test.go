package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/tararchive"
)

// Acceptance Test A: Concurrency limit 1 with two services waiting.
// Confirms that:
// 1. Exactly one service owns the active slot.
// 2. The other service has a visible queue position, wait duration, and slot owner.
// 3. After the first finishes, the next eligible service acquires the slot.
// 4. Cancellation removes a queued waiter cleanly.
// 5. Active and waiter counts return strictly to zero.
func TestAcceptance_A_TwoServicesWaitingForDockerSlots(t *testing.T) {
	sem := NewBuildSemaphore(1)
	assert.Equal(t, 1, sem.GetLimit())
	assert.Equal(t, 0, sem.GetActiveCount())
	assert.Equal(t, 0, sem.GetWaitersCount())

	ctx := context.Background()

	// 1. Service 1 acquires the slot
	s1Info := SlotOwnerInfo{
		ID:          "slot-s1",
		ServiceName: "ccms-backend",
		Phase:       "context_transfer",
	}
	rel1, err := sem.AcquireWithInfo(ctx, s1Info, nil)
	require.NoError(t, err)
	require.NotNil(t, rel1)

	assert.Equal(t, 1, sem.GetActiveCount())
	assert.Equal(t, 0, sem.GetWaitersCount())

	diag := sem.GetDiagnostics()
	require.Len(t, diag.ActiveSlots, 1)
	assert.Equal(t, "ccms-backend", diag.ActiveSlots[0].ServiceName)

	// 2. Service 2 attempts to acquire and must wait in queue
	s2Info := SlotOwnerInfo{
		ID:          "slot-s2",
		ServiceName: "ccms-frontend",
		Phase:       "queued",
	}

	s2Acquired := make(chan struct{})
	s2WaitReported := make(chan struct{})
	var observedPos int
	var observedOwner string

	go func() {
		rel2, err2 := sem.AcquireWithInfo(ctx, s2Info, func(pos int, waitDur time.Duration, actSlots []SlotOwnerInfo) {
			observedPos = pos
			if len(actSlots) > 0 {
				observedOwner = actSlots[0].ServiceName
			}
			select {
			case <-s2WaitReported:
			default:
				close(s2WaitReported)
			}
		})
		if err2 == nil {
			close(s2Acquired)
			rel2()
		}
	}()

	// Wait for queue wait callback to be invoked
	select {
	case <-s2WaitReported:
		assert.Equal(t, 1, observedPos, "ccms-frontend should be first in queue")
		assert.Equal(t, "ccms-backend", observedOwner, "slot owner should be ccms-backend")
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for queue wait callback")
	}

	assert.Equal(t, 1, sem.GetWaitersCount())

	// 3. Release Service 1 - Service 2 must immediately receive slot
	rel1()

	select {
	case <-s2Acquired:
		// Succeeded acquiring and releasing
	case <-time.After(2 * time.Second):
		t.Fatal("service 2 did not acquire slot after service 1 released")
	}

	// Wait brief moment for defer release in goroutine to complete
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, sem.GetActiveCount())
	assert.Equal(t, 0, sem.GetWaitersCount())

	// 4. Test cancellation of queued waiter
	relActive, err := sem.AcquireWithInfo(ctx, SlotOwnerInfo{ID: "hold", ServiceName: "hold"}, nil)
	require.NoError(t, err)

	cancellingCtx, cancel := context.WithCancel(context.Background())
	cancelErrCh := make(chan error, 1)

	go func() {
		_, err := sem.AcquireWithInfo(cancellingCtx, SlotOwnerInfo{ID: "waiter", ServiceName: "waiter"}, nil)
		cancelErrCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, sem.GetWaitersCount())

	// Cancel waiter
	cancel()
	select {
	case err := <-cancelErrCh:
		assert.True(t, errors.Is(err, context.Canceled))
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for cancelled acquire to return")
	}

	assert.Equal(t, 0, sem.GetWaitersCount(), "cancelled waiter must be removed without leaking")

	relActive()
	assert.Equal(t, 0, sem.GetActiveCount())
}

// Acceptance Test B: Source stream cancellation and unexpected EOF recovery.
func TestAcceptance_B_SourceStreamCancellationAndRecovery(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Simulate truncated stream
	truncatedData := []byte("incomplete-tar-header-not-valid")
	r := bytes.NewReader(truncatedData)

	file, err := tararchive.StageAndValidateArchiveStream(context.Background(), tempDir, r, "Dockerfile")
	assert.Error(t, err)
	assert.Nil(t, file)
	assert.Contains(t, err.Error(), "build context validation failed")

	// Ensure no leftover staged files
	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "temporary staged files must be cleaned up on failure")

	// 2. Simulate context cancellation during stream read
	pipeR, pipeW := io.Pipe()
	cancelCtx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		_ = pipeW.CloseWithError(context.Canceled)
	}()

	file2, err2 := tararchive.StageAndValidateArchiveStream(cancelCtx, tempDir, pipeR, "Dockerfile")
	assert.Error(t, err2)
	assert.Nil(t, file2)
	assert.Contains(t, err2.Error(), "cancelled")

	entries2, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, entries2, "temporary staged file must be cleaned up on cancellation")
}

// Acceptance Test C: Configurable Docker build timeout.
func TestAcceptance_C_TimeoutConfiguration(t *testing.T) {
	// Default when unset is 45 minutes
	_ = os.Unsetenv("FORGELAB_DOCKER_BUILD_TIMEOUT")
	assert.Equal(t, 45*time.Minute, getDockerBuildTimeout())

	// Configured via duration string
	_ = os.Setenv("FORGELAB_DOCKER_BUILD_TIMEOUT", "60m")
	assert.Equal(t, 60*time.Minute, getDockerBuildTimeout())

	// Configured via integer minutes
	_ = os.Setenv("FORGELAB_DOCKER_BUILD_TIMEOUT", "30")
	assert.Equal(t, 30*time.Minute, getDockerBuildTimeout())

	_ = os.Unsetenv("FORGELAB_DOCKER_BUILD_TIMEOUT")
}

// Acceptance Test D: Exact Dockerfile selection across nested and root directories.
func TestAcceptance_D_ExactDockerfileSelection(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add root Dockerfile
	rootContent := []byte("FROM alpine\n# Root Dockerfile\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "Dockerfile", Size: int64(len(rootContent)), Mode: 0644}))
	_, _ = tw.Write(rootContent)

	// Add frontend Dockerfile
	frontContent := []byte("FROM node:alpine\n# Frontend Dockerfile\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "frontend/Dockerfile", Size: int64(len(frontContent)), Mode: 0644}))
	_, _ = tw.Write(frontContent)

	// Add backend Dockerfile
	backContent := []byte("FROM golang:alpine\n# Backend Dockerfile\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "backend/Dockerfile", Size: int64(len(backContent)), Mode: 0644}))
	_, _ = tw.Write(backContent)

	require.NoError(t, tw.Close())
	tarBytes := buf.Bytes()

	// 1. Requesting root Dockerfile extracts only root
	extractedRoot, err := extractDockerfileFromTar(bytes.NewReader(tarBytes), "Dockerfile")
	require.NoError(t, err)
	assert.Contains(t, string(extractedRoot), "# Root Dockerfile")

	// 2. Requesting frontend/Dockerfile extracts only frontend
	extractedFront, err := extractDockerfileFromTar(bytes.NewReader(tarBytes), "frontend/Dockerfile")
	require.NoError(t, err)
	assert.Contains(t, string(extractedFront), "# Frontend Dockerfile")

	// 3. Requesting backend/Dockerfile extracts only backend
	extractedBack, err := extractDockerfileFromTar(bytes.NewReader(tarBytes), "backend/Dockerfile")
	require.NoError(t, err)
	assert.Contains(t, string(extractedBack), "# Backend Dockerfile")

	// 4. ValidateTarArchive requires exact match
	require.NoError(t, tararchive.ValidateTarArchive(bytes.NewReader(tarBytes), "frontend/Dockerfile"))
	require.NoError(t, tararchive.ValidateTarArchive(bytes.NewReader(tarBytes), "backend/Dockerfile"))
	require.NoError(t, tararchive.ValidateTarArchive(bytes.NewReader(tarBytes), "Dockerfile"))

	// Non-existent Dockerfile fails validation
	errNotFound := tararchive.ValidateTarArchive(bytes.NewReader(tarBytes), "nonexistent/Dockerfile")
	assert.Error(t, errNotFound)
	assert.True(t, errors.Is(errNotFound, tararchive.ErrDockerfileMissing))
}
