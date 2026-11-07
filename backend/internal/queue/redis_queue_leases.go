package queue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// acquireLease atomically acquires an exclusive execution lease for a job AND sets its
// state to "processing" in a single Lua script. This prevents the race where a worker
// crashes between acquiring the lease and setting the state index.
// Returns a unique execution lease token if acquired.
func (q *DeploymentQueue) acquireLease(ctx context.Context, jobID uuid.UUID, workerID string) (string, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID
	token := fmt.Sprintf("%s:%s", workerID, uuid.New().String())

	res, err := acquireLeaseScript.Run(ctx, q.client,
		[]string{leaseKey, stateKey},
		token, q.leaseTTL.Milliseconds(), JobStateProcessing, int64(24*time.Hour/time.Second),
	).Result()
	if err != nil {
		return token, false, fmt.Errorf("failed to execute acquire lease script: %w", err)
	}
	acquired, ok := res.(int64)
	if !ok || acquired == 0 {
		return "", false, nil
	}

	return token, true, nil
}

// extendLease renews the TTL of the worker's lease ONLY if caller still owns the matching token.
func (q *DeploymentQueue) extendLease(ctx context.Context, jobID uuid.UUID, leaseToken string) error {
	leaseKey := JobLeasePrefix + jobID.String()
	res, err := extendLeaseScript.Run(ctx, q.client, []string{leaseKey}, leaseToken, q.leaseTTL.Milliseconds()).Result()
	if err != nil {
		return fmt.Errorf("failed to execute lease extension script: %w", err)
	}

	renewed, ok := res.(int64)
	if !ok || renewed == 0 {
		return ErrLeaseLost
	}
	return nil
}

// releaseLease deletes the lease ONLY if caller still owns the matching token.
func (q *DeploymentQueue) releaseLease(ctx context.Context, jobID uuid.UUID, leaseToken string) {
	leaseKey := JobLeasePrefix + jobID.String()
	_, _ = releaseLeaseScript.Run(ctx, q.client, []string{leaseKey}, leaseToken).Result()
}

// FinalizeSuccess atomically removes the job from the processing queue, deletes the lease,
// deletes attempts, and clears the state index only if caller holds the matching lease token.
func (q *DeploymentQueue) FinalizeSuccess(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := finalizeSuccessScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return false, fmt.Errorf("failed to execute finalize success script: %w", err)
	}
	val, ok := res.(int64)
	return ok && val == 1, nil
}

// FailAndRequeue atomically removes the job from processing queue, deletes the lease,
// increments attempts, sets state to pending, and re-enqueues into the deployment queue
// only if caller holds the matching lease token. Returns (newAttempts, success, error).
func (q *DeploymentQueue) FailAndRequeue(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := failAndRequeueScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeploymentQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr, JobStatePending,
	).Result()
	if err != nil {
		return 0, false, fmt.Errorf("failed to execute fail and requeue script: %w", err)
	}
	attempts, ok := res.(int64)
	if !ok || attempts < 0 {
		return 0, false, nil
	}
	return attempts, true, nil
}

// FailToDLQ atomically removes the job from processing queue, deletes the lease,
// increments attempts, sets state to dead_letter, and transfers to dead-letter queue
// only if caller holds the matching lease token. Returns (newAttempts, success, error).
func (q *DeploymentQueue) FailToDLQ(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := failToDLQScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeadLetterQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr, JobStateDLQ,
	).Result()
	if err != nil {
		return 0, false, fmt.Errorf("failed to execute fail to DLQ script: %w", err)
	}
	attempts, ok := res.(int64)
	if !ok || attempts < 0 {
		return 0, false, nil
	}
	return attempts, true, nil
}

// TerminalCleanup atomically removes the job from processing queue, deletes the lease,
// and deletes state only if caller holds the matching lease token. If moveToDLQ is true, pushes to DLQ.
func (q *DeploymentQueue) TerminalCleanup(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string, moveToDLQ bool) (bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID

	keys := []string{leaseKey, ProcessingQueueKey, stateKey}
	if moveToDLQ {
		keys = append(keys, DeadLetterQueueKey)
	}

	res, err := terminalCleanupScript.Run(ctx, q.client,
		keys,
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return false, fmt.Errorf("failed to execute terminal cleanup script: %w", err)
	}
	val, ok := res.(int64)
	return ok && val == 1, nil
}

// LRemProcessingWithLease atomically removes item from processing queue only if leaseToken matches.
func (q *DeploymentQueue) LRemProcessingWithLease(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	res, err := lremProcessingWithLeaseScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey},
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return 0, err
	}
	rem, ok := res.(int64)
	if !ok || rem < 0 {
		return 0, ErrLeaseLost
	}
	return rem, nil
}

// IsLeaseOwner checks if the given leaseToken currently owns the job lease.
func (q *DeploymentQueue) IsLeaseOwner(ctx context.Context, jobID uuid.UUID, leaseToken string) (bool, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	val, err := q.client.Get(ctx, leaseKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	return val == leaseToken, nil
}

// HasActiveLease returns true if an active, non-expired worker lease exists for the job.
func (q *DeploymentQueue) HasActiveLease(ctx context.Context, jobID uuid.UUID) (bool, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	n, err := q.client.Exists(ctx, leaseKey).Result()
	return n > 0, err
}

// IsJobInQueue checks if the job is waiting in the pending queue.
// Checks explicit O(1) state key first, then falls back to list scan if not indexed.
func (q *DeploymentQueue) IsJobInQueue(ctx context.Context, jobID uuid.UUID) (bool, error) {
	length, err := q.client.LLen(ctx, DeploymentQueueKey).Result()
	if err != nil || length == 0 {
		return false, err
	}

	state, err := q.client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err == nil && state == JobStatePending {
		return true, nil
	}

	// Fallback to scanning queue list (e.g. for items pushed directly in tests)
	items, err := q.client.LRange(ctx, DeploymentQueueKey, 0, -1).Result()
	if err != nil {
		return false, err
	}
	idStr := jobID.String()
	for _, item := range items {
		if job, err := ParseJob(item); err == nil && job.ID.String() == idStr {
			return true, nil
		}
	}
	return false, nil
}

// IsJobInProcessing checks if the job is recorded in the processing queue or active lease.
// Checks explicit O(1) state key first, then falls back to list scan if not indexed.
func (q *DeploymentQueue) IsJobInProcessing(ctx context.Context, jobID uuid.UUID) (bool, error) {
	state, err := q.client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err == nil && state == JobStateProcessing {
		return true, nil
	}

	length, err := q.client.LLen(ctx, ProcessingQueueKey).Result()
	if err != nil || length == 0 {
		return false, err
	}

	// Fallback to scanning processing list
	items, err := q.client.LRange(ctx, ProcessingQueueKey, 0, -1).Result()
	if err != nil {
		return false, err
	}
	idStr := jobID.String()
	for _, item := range items {
		if job, err := ParseJob(item); err == nil && job.ID.String() == idStr {
			return true, nil
		}
	}
	return false, nil
}

// IsJobEnqueuedOrActive returns true if the job is either in pending queue, processing queue, or has an active lease.
func (q *DeploymentQueue) IsJobEnqueuedOrActive(ctx context.Context, jobID uuid.UUID) (bool, error) {
	active, err := q.HasActiveLease(ctx, jobID)
	if err == nil && active {
		return true, nil
	}

	inQueue, err := q.IsJobInQueue(ctx, jobID)
	if err == nil && inQueue {
		return true, nil
	}
	inProc, err := q.IsJobInProcessing(ctx, jobID)
	if err == nil && inProc {
		return true, nil
	}
	return false, nil
}

// GetAttempts returns the number of execution attempts for a given job.
func (q *DeploymentQueue) GetAttempts(ctx context.Context, jobID uuid.UUID) (int, error) {
	attemptsStr, err := q.client.Get(ctx, JobAttemptsPrefix+jobID.String()).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, err
	}
	return strconv.Atoi(attemptsStr)
}

// IncrementAttempts increments the retry attempt counter in Redis.
func (q *DeploymentQueue) IncrementAttempts(ctx context.Context, jobID uuid.UUID) (int64, error) {
	key := JobAttemptsPrefix + jobID.String()
	n, err := q.client.Incr(ctx, key).Result()
	if err == nil {
		q.client.Expire(ctx, key, 24*time.Hour)
	}
	return n, err
}

// ResetAttempts clears the attempt counter for a job.
func (q *DeploymentQueue) ResetAttempts(ctx context.Context, jobID uuid.UUID) error {
	return q.client.Del(ctx, JobAttemptsPrefix+jobID.String()).Err()
}
