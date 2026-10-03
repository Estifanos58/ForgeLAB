package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	return mr, client
}

func TestDeploymentQueue_EnqueueAndProcess(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	depID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processedCount int32
	done := make(chan struct{})

	q.StartWorker(ctx, func(wCtx context.Context, id uuid.UUID) error {
		if id == depID {
			atomic.AddInt32(&processedCount, 1)
			close(done)
		}
		return nil
	})
	defer q.Stop()

	// Enqueue job
	if err := q.EnqueueDeployment(ctx, depID); err != nil {
		t.Fatalf("failed to enqueue deployment: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for worker to process job")
	}

	if atomic.LoadInt32(&processedCount) != 1 {
		t.Errorf("expected 1 processed job, got %d", processedCount)
	}

	// Verify processing queue is empty after clean completion
	var pending, processing, dlq int64
	var err error
	for i := 0; i < 25; i++ {
		pending, processing, dlq, err = q.GetQueueStats(ctx)
		if err == nil && pending == 0 && processing == 0 && dlq == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if pending != 0 || processing != 0 || dlq != 0 {
		t.Errorf("expected all queues empty, got pending=%d, processing=%d, dlq=%d", pending, processing, dlq)
	}
}

func TestDeploymentQueue_PayloadTypeDisambiguation(t *testing.T) {
	// Plain UUID string must be rejected to prevent release vs service deployment confusion
	rawUUID := uuid.New().String()
	_, err := ParseJob(rawUUID)
	if err == nil {
		t.Fatalf("expected ParseJob to reject legacy plain UUID, but it succeeded")
	}
	if !errors.Is(err, ErrInvalidJobPayload) {
		t.Errorf("expected ErrInvalidJobPayload, got %v", err)
	}

	// Invalid JSON must be rejected
	_, err = ParseJob("{invalid json")
	if err == nil {
		t.Fatalf("expected ParseJob to reject malformed JSON")
	}

	// Unknown job type must be rejected
	unknownJobJSON := `{"type": "unknown_type", "id": "` + rawUUID + `"}`
	_, err = ParseJob(unknownJobJSON)
	if err == nil {
		t.Fatalf("expected ParseJob to reject unknown job type")
	}

	// Valid service deployment job
	svcID := uuid.New()
	svcJob := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  svcID,
		ServiceDeploymentID: svcID,
	}
	parsedSvc, err := ParseJob(svcJob.Encode())
	if err != nil {
		t.Fatalf("failed to parse valid service deployment job: %v", err)
	}
	if parsedSvc.Type != JobTypeServiceDeployment || parsedSvc.ID != svcID {
		t.Errorf("mismatched service job: %+v", parsedSvc)
	}

	// Valid release deployment job
	relID := uuid.New()
	relJob := Job{
		Type:         JobTypeDeployment,
		ID:           relID,
		DeploymentID: &relID,
	}
	parsedRel, err := ParseJob(relJob.Encode())
	if err != nil {
		t.Fatalf("failed to parse valid release deployment job: %v", err)
	}
	if parsedRel.Type != JobTypeDeployment || parsedRel.ID != relID {
		t.Errorf("mismatched release job: %+v", parsedRel)
	}
}

func TestDeploymentQueue_CrashRecovery_WithLease(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	crashedID := uuid.New()

	ctx := context.Background()

	// Simulate a typed job left in processing queue due to worker crash (lease expired)
	job := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  crashedID,
		ServiceDeploymentID: crashedID,
	}
	err := client.LPush(ctx, ProcessingQueueKey, job.Encode()).Err()
	if err != nil {
		t.Fatalf("failed to seed processing queue: %v", err)
	}

	// Run recovery sweep (no active lease exists, so it's recognized as abandoned)
	recovered, err := q.RecoverAbandonedJobs(ctx)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if recovered != 1 {
		t.Errorf("expected 1 job recovered, got %d", recovered)
	}

	// Verify job was moved from processing back to main deployment queue
	pending, processing, _, _ := q.GetQueueStats(ctx)
	if processing != 0 {
		t.Errorf("expected 0 in processing after recovery, got %d", processing)
	}
	if pending != 1 {
		t.Errorf("expected 1 in pending after recovery, got %d", pending)
	}

	// Verify attempt was incremented during recovery
	attempts, err := q.GetAttempts(ctx, crashedID)
	if err != nil || attempts != 1 {
		t.Errorf("expected attempts=1, got %d (err: %v)", attempts, err)
	}
}

func TestDeploymentQueue_ActiveLeaseNotRecovered(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	activeID := uuid.New()
	ctx := context.Background()

	job := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  activeID,
		ServiceDeploymentID: activeID,
	}
	_ = client.LPush(ctx, ProcessingQueueKey, job.Encode())

	// Simulate an active worker lease
	leaseKey := JobLeasePrefix + activeID.String()
	_ = client.Set(ctx, leaseKey, "worker-1", 60*time.Second)

	// Run recovery - active lease must prevent recovery
	recovered, err := q.RecoverAbandonedJobs(ctx)
	if err != nil {
		t.Fatalf("recovery error: %v", err)
	}
	if recovered != 0 {
		t.Errorf("expected 0 jobs recovered while lease is active, got %d", recovered)
	}

	_, processing, _, _ := q.GetQueueStats(ctx)
	if processing != 1 {
		t.Errorf("expected job to remain in processing queue, got %d", processing)
	}
}

func TestDeploymentQueue_TerminalJobNeverRetried(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetRetryBackoff(10 * time.Millisecond)
	terminalID := uuid.New()

	// Terminal checker reports this job is terminal
	q.SetTerminalChecker(func(ctx context.Context, job Job) (bool, error) {
		if job.ID == terminalID {
			return true, nil
		}
		return false, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var executionAttempts int32
	dlqDone := make(chan struct{})

	q.StartJobWorker(ctx, func(wCtx context.Context, job Job) error {
		if job.ID == terminalID {
			atomic.AddInt32(&executionAttempts, 1)
			go func() {
				for i := 0; i < 20; i++ {
					time.Sleep(50 * time.Millisecond)
					_, _, dlq, _ := q.GetQueueStats(ctx)
					if dlq >= 1 {
						select {
						case <-dlqDone:
						default:
							close(dlqDone)
						}
						return
					}
				}
			}()
			return errors.New("terminal error")
		}
		return nil
	})
	defer q.Stop()

	if err := q.EnqueueServiceDeployment(ctx, terminalID); err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	select {
	case <-dlqDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for terminal job to move to DLQ")
	}

	// Must only have executed ONCE — never retried because it's terminal!
	if attempts := atomic.LoadInt32(&executionAttempts); attempts != 1 {
		t.Errorf("expected exactly 1 execution attempt for terminal job, got %d", attempts)
	}

	pending, processing, dlq, _ := q.GetQueueStats(ctx)
	if pending != 0 || processing != 0 || dlq != 1 {
		t.Errorf("expected pending=0, processing=0, dlq=1; got pending=%d, processing=%d, dlq=%d", pending, processing, dlq)
	}
}

func TestDeploymentQueue_DuplicateExecutionPrevention(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetWorkerCount(4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	duplicateID := uuid.New()
	var activeConcurrent int32
	var maxConcurrent int32
	var totalProcessed int32
	var mu sync.Mutex

	jobStarted := make(chan struct{})
	allowFinish := make(chan struct{})

	q.StartJobWorker(ctx, func(wCtx context.Context, job Job) error {
		if job.ID == duplicateID {
			curr := atomic.AddInt32(&activeConcurrent, 1)
			mu.Lock()
			if curr > maxConcurrent {
				maxConcurrent = curr
			}
			mu.Unlock()

			// Signal that one worker has started
			select {
			case <-jobStarted:
			default:
				close(jobStarted)
			}

			// Hold the worker until released
			select {
			case <-allowFinish:
			case <-wCtx.Done():
			}

			atomic.AddInt32(&activeConcurrent, -1)
			atomic.AddInt32(&totalProcessed, 1)
		}
		return nil
	})
	defer q.Stop()

	// Enqueue the SAME job ID twice
	_ = q.EnqueueServiceDeployment(ctx, duplicateID)
	_ = q.EnqueueServiceDeployment(ctx, duplicateID)

	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for first worker to start")
	}

	// Give other workers a chance to see if they can concurrently acquire the same job
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	peak := maxConcurrent
	mu.Unlock()

	if peak > 1 {
		t.Fatalf("duplicate execution detected: max concurrent executions was %d, expected 1", peak)
	}

	// Release the worker
	close(allowFinish)
	time.Sleep(200 * time.Millisecond)
}

func TestDeploymentQueue_RetryAndDeadLetterQueue(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetMaxRetries(2)
	q.SetRetryBackoff(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failingID := uuid.New()
	var attempts int32
	dlqDone := make(chan struct{})

	q.StartWorker(ctx, func(wCtx context.Context, id uuid.UUID) error {
		if id == failingID {
			curr := atomic.AddInt32(&attempts, 1)
			if curr >= 2 {
				go func() {
					for i := 0; i < 20; i++ {
						time.Sleep(50 * time.Millisecond)
						_, _, dlq, _ := q.GetQueueStats(ctx)
						if dlq >= 1 {
							close(dlqDone)
							return
						}
					}
				}()
			}
			return errors.New("simulated transient docker failure")
		}
		return nil
	})
	defer q.Stop()

	if err := q.EnqueueDeployment(ctx, failingID); err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	select {
	case <-dlqDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for job to land in dead letter queue")
	}

	_, processing, dlq, _ := q.GetQueueStats(ctx)
	if processing != 0 {
		t.Errorf("expected 0 processing, got %d", processing)
	}
	if dlq != 1 {
		t.Errorf("expected 1 in dead letter queue, got %d", dlq)
	}
}

func TestDeploymentQueue_ConcurrentServiceDeployments(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetWorkerCount(4)
	q.SetMaxRetries(1) // fail fast to DLQ

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frontendID := uuid.New()
	backendID := uuid.New()

	backendCompleted := make(chan struct{})
	frontendFailed := make(chan struct{})

	q.StartJobWorker(ctx, func(wCtx context.Context, job Job) error {
		if job.ID == frontendID {
			close(frontendFailed)
			return errors.New("frontend build syntax error")
		}
		if job.ID == backendID {
			time.Sleep(100 * time.Millisecond)
			close(backendCompleted)
			return nil
		}
		return nil
	})
	defer q.Stop()

	if err := q.EnqueueServiceDeployment(ctx, frontendID); err != nil {
		t.Fatalf("failed to enqueue frontend: %v", err)
	}
	if err := q.EnqueueServiceDeployment(ctx, backendID); err != nil {
		t.Fatalf("failed to enqueue backend: %v", err)
	}

	select {
	case <-backendCompleted:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for backend deployment to complete successfully")
	}

	select {
	case <-frontendFailed:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for frontend deployment to fail")
	}
}

func TestDeploymentQueue_IsJobEnqueuedOrActive(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	ctx := context.Background()

	jobID := uuid.New()

	// Initially not enqueued or active
	active, err := q.IsJobEnqueuedOrActive(ctx, jobID)
	if err != nil || active {
		t.Fatalf("expected false, got %v (err: %v)", active, err)
	}

	// Enqueue
	_ = q.EnqueueServiceDeployment(ctx, jobID)
	active, err = q.IsJobEnqueuedOrActive(ctx, jobID)
	if err != nil || !active {
		t.Fatalf("expected true after enqueue, got %v (err: %v)", active, err)
	}

	// Move to processing
	_, _ = client.RPopLPush(ctx, DeploymentQueueKey, ProcessingQueueKey).Result()
	active, err = q.IsJobEnqueuedOrActive(ctx, jobID)
	if err != nil || !active {
		t.Fatalf("expected true while in processing, got %v (err: %v)", active, err)
	}

	// Clear processing, set lease
	client.Del(ctx, ProcessingQueueKey)
	leaseKey := JobLeasePrefix + jobID.String()
	client.Set(ctx, leaseKey, "worker-1", 60*time.Second)
	active, err = q.IsJobEnqueuedOrActive(ctx, jobID)
	if err != nil || !active {
		t.Fatalf("expected true while active lease exists, got %v (err: %v)", active, err)
	}

	// Clear lease
	client.Del(ctx, leaseKey)
	active, err = q.IsJobEnqueuedOrActive(ctx, jobID)
	if err != nil || active {
		t.Fatalf("expected false after all cleared, got %v (err: %v)", active, err)
	}
}

func TestDeploymentQueue_EnqueueFailure(t *testing.T) {
	mr, client := setupTestRedis(t)
	q := NewDeploymentQueue(client)
	jobID := uuid.New()

	// Close miniredis server to force connection failure
	mr.Close()
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := q.EnqueueServiceDeployment(ctx, jobID)
	if err == nil {
		t.Fatalf("expected enqueue to fail immediately on broken Redis connection, but it succeeded")
	}

	err = q.EnqueueDeployment(ctx, jobID)
	if err == nil {
		t.Fatalf("expected release deployment enqueue to fail immediately on broken Redis connection, but it succeeded")
	}
}

func TestDeploymentQueue_LeaseExpiryAndRecovery(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetLeaseTTL(1 * time.Second) // 1s minimal Redis EXPIRE duration
	ctx := context.Background()

	jobID := uuid.New()
	job := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  jobID,
		ServiceDeploymentID: jobID,
	}

	// Job is currently in processing queue with an active lease
	_ = client.LPush(ctx, ProcessingQueueKey, job.Encode())
	_, acquired, err := q.acquireLease(ctx, jobID, "worker-test-crash")
	if err != nil || !acquired {
		t.Fatalf("failed to acquire test lease: %v", err)
	}

	// While lease is unexpired: recovery must NOT touch it
	rec, err := q.RecoverAbandonedJobs(ctx)
	if err != nil {
		t.Fatalf("recovery error: %v", err)
	}
	if rec != 0 {
		t.Errorf("expected 0 jobs recovered while lease is alive, got %d", rec)
	}

	// Advance miniredis clock beyond lease TTL (simulate worker crash without heartbeat)
	mr.FastForward(2 * time.Second)

	// Now lease has expired! Recovery must recover the abandoned job
	rec, err = q.RecoverAbandonedJobs(ctx)
	if err != nil {
		t.Fatalf("recovery error: %v", err)
	}
	if rec != 1 {
		t.Errorf("expected 1 abandoned job recovered after lease expiry, got %d", rec)
	}

	// Verify job is back in pending queue
	pending, processing, _, _ := q.GetQueueStats(ctx)
	if pending != 1 || processing != 0 {
		t.Errorf("expected pending=1, processing=0; got pending=%d, processing=%d", pending, processing)
	}
}

func TestDeploymentQueue_LeaseOwnershipSafety(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetLeaseTTL(2 * time.Second)
	ctx := context.Background()

	jobID := uuid.New()

	// 1. Worker 1 acquires lease
	token1, acquired, err := q.acquireLease(ctx, jobID, "worker-1")
	if err != nil || !acquired {
		t.Fatalf("worker-1 failed to acquire lease: %v", err)
	}
	if token1 == "" {
		t.Fatalf("expected non-empty lease token for worker-1")
	}

	// 2. Worker 2 attempts to acquire lease while worker-1 is active -> MUST FAIL
	token2, acquired2, err := q.acquireLease(ctx, jobID, "worker-2")
	if err != nil {
		t.Fatalf("worker-2 unexpected error: %v", err)
	}
	if acquired2 || token2 != "" {
		t.Fatalf("worker-2 should not acquire active lease")
	}

	// 3. Worker 1 extends its lease with token1 -> MUST SUCCEED
	if err := q.extendLease(ctx, jobID, token1); err != nil {
		t.Fatalf("worker-1 failed to extend lease: %v", err)
	}

	// 4. An imposter with wrong token attempts to extend -> MUST FAIL with ErrLeaseLost
	err = q.extendLease(ctx, jobID, "imposter-token")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost for imposter extension, got %v", err)
	}

	// 5. Worker 1's lease expires (simulate worker-1 hung or crashed)
	mr.FastForward(3 * time.Second)

	// 6. Replacement worker-3 acquires lease with new token
	token3, acquired3, err := q.acquireLease(ctx, jobID, "worker-3")
	if err != nil || !acquired3 {
		t.Fatalf("worker-3 failed to acquire expired lease: %v", err)
	}

	// 7. Expired worker-1 wakes up and attempts to extend its OLD lease -> MUST FAIL with ErrLeaseLost
	err = q.extendLease(ctx, jobID, token1)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost for expired worker-1, got %v", err)
	}

	// 8. Expired worker-1 attempts to release its OLD lease -> MUST NOT delete worker-3's lease!
	q.releaseLease(ctx, jobID, token1)

	// Verify worker-3 still holds an active lease!
	hasActive, err := q.HasActiveLease(ctx, jobID)
	if err != nil || !hasActive {
		t.Fatalf("expected worker-3's lease to remain intact after worker-1 release attempt, got active=%v, err=%v", hasActive, err)
	}

	// 9. Worker 3 extends its lease -> MUST SUCCEED
	if err := q.extendLease(ctx, jobID, token3); err != nil {
		t.Fatalf("worker-3 failed to extend lease: %v", err)
	}

	// 10. Worker 3 releases with its valid token -> MUST SUCCEED
	q.releaseLease(ctx, jobID, token3)

	hasActiveAfterRelease, err := q.HasActiveLease(ctx, jobID)
	if err != nil || hasActiveAfterRelease {
		t.Fatalf("expected lease to be released by worker-3, got active=%v", hasActiveAfterRelease)
	}
}

func TestDeploymentQueue_JobStateIndexing(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	ctx := context.Background()
	jobID := uuid.New()

	// Initially not enqueued
	inQueue, err := q.IsJobInQueue(ctx, jobID)
	if err != nil || inQueue {
		t.Fatalf("expected not in queue, got %v", inQueue)
	}

	// Enqueue -> state set to pending (O(1))
	if err := q.EnqueueServiceDeployment(ctx, jobID); err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	state, err := client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err != nil || state != JobStatePending {
		t.Fatalf("expected state pending, got %v, err: %v", state, err)
	}

	inQueue, err = q.IsJobInQueue(ctx, jobID)
	if err != nil || !inQueue {
		t.Fatalf("expected in queue, got %v", inQueue)
	}

	// Acquire lease -> state set to processing (O(1))
	token, acquired, err := q.acquireLease(ctx, jobID, "worker-1")
	if err != nil || !acquired {
		t.Fatalf("failed to acquire lease: %v", err)
	}

	state, err = client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err != nil || state != JobStateProcessing {
		t.Fatalf("expected state processing, got %v, err: %v", state, err)
	}

	inProc, err := q.IsJobInProcessing(ctx, jobID)
	if err != nil || !inProc {
		t.Fatalf("expected in processing, got %v", inProc)
	}

	// Release
	q.releaseLease(ctx, jobID, token)
}

func TestDeploymentQueue_Fencing_StaleWorkerCannotMutateJob(t *testing.T) {
	runScenario := func(t *testing.T, aFails bool) {
		mr, client := setupTestRedis(t)
		defer mr.Close()
		defer client.Close()

		q := NewDeploymentQueue(client)
		q.SetWorkerCount(1)
		q.SetLeaseTTL(1 * time.Second)
		q.SetHeartbeatInterval(10 * time.Second) // Long heartbeat so Worker A doesn't auto-renew

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		jobID := uuid.New()
		job := Job{
			Type:                JobTypeServiceDeployment,
			ID:                  jobID,
			ServiceDeploymentID: jobID,
		}

		startedA := make(chan struct{})
		continueA := make(chan struct{})
		finishedA := make(chan struct{})

		// Handler for Worker A: pauses in the middle of execution
		q.StartJobWorker(ctx, func(wCtx context.Context, j Job) error {
			if j.ID == jobID {
				close(startedA)
				<-continueA
				defer close(finishedA)
				if aFails {
					return errors.New("worker A failed late after lease expiration")
				}
				return nil
			}
			return nil
		})
		defer q.Stop()

		// 1. Enqueue job
		if err := q.EnqueueServiceDeployment(ctx, jobID); err != nil {
			t.Fatalf("failed to enqueue: %v", err)
		}

		// 2. Wait for Worker A to acquire lease and start execution
		select {
		case <-startedA:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for Worker A to start")
		}

		leaseKey := JobLeasePrefix + jobID.String()
		tokenA, err := client.Get(ctx, leaseKey).Result()
		if err != nil || tokenA == "" {
			t.Fatalf("expected Worker A to hold lease token, got %v, err=%v", tokenA, err)
		}

		// 3. Worker A loses its lease (simulate lease TTL expiring due to hang/partition)
		mr.FastForward(2 * time.Second)

		// Verify lease expired in Redis
		hasLease, _ := q.HasActiveLease(ctx, jobID)
		if hasLease {
			t.Fatalf("expected lease to have expired")
		}

		// 4. Recovery requeues the abandoned job from processing queue to pending queue
		rec, err := q.RecoverAbandonedJobs(ctx)
		if err != nil {
			t.Fatalf("failed to recover abandoned job: %v", err)
		}
		if rec != 1 {
			t.Fatalf("expected 1 recovered job, got %d", rec)
		}

		// Verify job is back in pending queue
		pendingCount, err := client.LLen(ctx, DeploymentQueueKey).Result()
		if err != nil || pendingCount != 1 {
			t.Fatalf("expected pending queue len 1, got %d", pendingCount)
		}

		// 5. Worker B acquires the lease and starts processing
		tokenB, acquiredB, err := q.acquireLease(ctx, jobID, "worker-B")
		if err != nil || !acquiredB {
			t.Fatalf("worker-B failed to acquire lease: %v", err)
		}
		if tokenB == "" || tokenB == tokenA {
			t.Fatalf("expected unique lease token for worker-B, got %v", tokenB)
		}

		// Put Worker B's execution into processing queue (simulating pop & process by worker B)
		client.LRem(ctx, DeploymentQueueKey, 1, job.Encode())
		client.LPush(ctx, ProcessingQueueKey, job.Encode())

		attemptsBeforeA, _ := client.Get(ctx, JobAttemptsPrefix+jobID.String()).Result()

		// 6. Worker A finishes late!
		close(continueA)

		select {
		case <-finishedA:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for Worker A to finish")
		}

		// Grace period for any asynchronous operations
		time.Sleep(50 * time.Millisecond)

		// 7. VERIFY INVARIANTS:
		// - Worker B remains the sole lease owner
		currentLeaseToken, err := client.Get(ctx, leaseKey).Result()
		if err != nil {
			t.Fatalf("expected active lease for worker-B, got err: %v", err)
		}
		if currentLeaseToken != tokenB {
			t.Fatalf("expected worker-B to remain lease owner (%s), but got %s", tokenB, currentLeaseToken)
		}

		// - Worker A did NOT remove Worker B's processing entry
		procLen, err := client.LLen(ctx, ProcessingQueueKey).Result()
		if err != nil || procLen != 1 {
			t.Fatalf("expected processing queue to retain worker-B entry (len 1), got %d (err: %v)", procLen, err)
		}

		// - Worker A did NOT modify retry attempts (neither incremented on error nor deleted on success)
		attemptsAfterA, _ := client.Get(ctx, JobAttemptsPrefix+jobID.String()).Result()
		if attemptsAfterA != attemptsBeforeA {
			t.Fatalf("expected attempts to not be modified by stale worker A; before=%q, after=%q", attemptsBeforeA, attemptsAfterA)
		}

		// - Worker A did NOT requeue the job into pending queue
		pendingAfter, err := client.LLen(ctx, DeploymentQueueKey).Result()
		if err != nil || pendingAfter != 0 {
			t.Fatalf("expected pending queue to remain 0, got %d", pendingAfter)
		}

		// - Worker A did NOT move the job to dead-letter queue
		dlqAfter, err := client.LLen(ctx, DeadLetterQueueKey).Result()
		if err != nil || dlqAfter != 0 {
			t.Fatalf("expected dead letter queue to remain 0, got %d", dlqAfter)
		}

		// - Worker A did NOT change job state from processing
		stateAfter, err := client.Get(ctx, JobStatePrefix+jobID.String()).Result()
		if err != nil || stateAfter != JobStateProcessing {
			t.Fatalf("expected job state to remain processing for worker-B, got %v (err: %v)", stateAfter, err)
		}
	}

	t.Run("Worker A finishes late with error", func(t *testing.T) {
		runScenario(t, true)
	})

	t.Run("Worker A finishes late with success", func(t *testing.T) {
		runScenario(t, false)
	})
}
