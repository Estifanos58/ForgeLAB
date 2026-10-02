package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	DeploymentQueueKey       = "forgelab:queue:deployments"
	ProcessingQueueKey       = "forgelab:queue:deployments:processing"
	DeadLetterQueueKey       = "forgelab:queue:deployments:dead_letter"
	JobProcessingPrefix      = "forgelab:job:processing:"
	JobAttemptsPrefix        = "forgelab:job:attempts:"
	JobLeasePrefix           = "forgelab:job:lease:"
	DefaultMaxRetries        = 3
	DefaultRetryBackoff      = 3 * time.Second
	DefaultWorkerCount       = 4
	DefaultLeaseTTL          = 60 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second
)

var (
	ErrJobAlreadyProcessing = errors.New("deployment job is already being processed")
	ErrInvalidJobPayload    = errors.New("invalid job payload: strictly typed JSON payload is required")
)

type JobType string

const (
	JobTypeServiceDeployment JobType = "service_deployment"
	JobTypeDeployment        JobType = "deployment"
)

// Job represents a queued deployment unit with explicit typing.
type Job struct {
	Type                JobType    `json:"type"`
	ID                  uuid.UUID  `json:"id"`
	ServiceDeploymentID uuid.UUID  `json:"service_deployment_id,omitempty"`
	DeploymentID        *uuid.UUID `json:"deployment_id,omitempty"`
	ProjectID           *uuid.UUID `json:"project_id,omitempty"`
}

// Encode serializes the job to a JSON string.
func (j Job) Encode() string {
	b, _ := json.Marshal(j)
	return string(b)
}

// ParseJob decodes a queue item string into a strictly typed Job.
// Untyped or legacy plain UUID strings are rejected to eliminate ambiguity
// between service deployments and release deployments.
func ParseJob(item string) (Job, error) {
	trimmed := strings.TrimSpace(item)
	if !strings.HasPrefix(trimmed, "{") {
		return Job{}, fmt.Errorf("%w: untyped plain UUID representation is rejected to prevent release vs service deployment confusion", ErrInvalidJobPayload)
	}

	var j Job
	if err := json.Unmarshal([]byte(trimmed), &j); err != nil {
		return Job{}, fmt.Errorf("%w: unmarshal failed: %v", ErrInvalidJobPayload, err)
	}

	if j.Type != JobTypeServiceDeployment && j.Type != JobTypeDeployment {
		return Job{}, fmt.Errorf("%w: unknown or missing job type %q", ErrInvalidJobPayload, j.Type)
	}

	if j.ID == uuid.Nil {
		if j.ServiceDeploymentID != uuid.Nil {
			j.ID = j.ServiceDeploymentID
		} else if j.DeploymentID != nil && *j.DeploymentID != uuid.Nil {
			j.ID = *j.DeploymentID
		} else {
			return Job{}, fmt.Errorf("%w: missing deployment ID", ErrInvalidJobPayload)
		}
	}

	return j, nil
}

// TerminalChecker is an optional function that reports whether a job is already in a terminal state.
type TerminalChecker func(ctx context.Context, job Job) (bool, error)

type DeploymentQueue struct {
	client            *redis.Client
	maxRetries        int
	workerCount       int
	retryBackoff      time.Duration
	leaseTTL          time.Duration
	heartbeatInterval time.Duration
	terminalChecker   TerminalChecker
	wg                sync.WaitGroup
	cancel            context.CancelFunc
}

func NewDeploymentQueue(client *redis.Client) *DeploymentQueue {
	return &DeploymentQueue{
		client:            client,
		maxRetries:        DefaultMaxRetries,
		workerCount:       DefaultWorkerCount,
		retryBackoff:      DefaultRetryBackoff,
		leaseTTL:          DefaultLeaseTTL,
		heartbeatInterval: DefaultHeartbeatInterval,
	}
}

// SetTerminalChecker configures a callback to check if a job is in a terminal state before retrying.
func (q *DeploymentQueue) SetTerminalChecker(tc TerminalChecker) {
	q.terminalChecker = tc
}

// SetMaxRetries configures the maximum retry attempts for a failing deployment job.
func (q *DeploymentQueue) SetMaxRetries(n int) {
	if n > 0 {
		q.maxRetries = n
	}
}

// SetWorkerCount sets the number of concurrent worker goroutines.
func (q *DeploymentQueue) SetWorkerCount(n int) {
	if n > 0 {
		q.workerCount = n
	}
}

// SetRetryBackoff sets the backoff duration before retrying failed jobs.
func (q *DeploymentQueue) SetRetryBackoff(d time.Duration) {
	if d > 0 {
		q.retryBackoff = d
	}
}

// SetLeaseTTL sets the expiration time for worker leases.
func (q *DeploymentQueue) SetLeaseTTL(ttl time.Duration) {
	if ttl > 0 {
		q.leaseTTL = ttl
	}
}

// SetHeartbeatInterval sets the heartbeat renewal frequency.
func (q *DeploymentQueue) SetHeartbeatInterval(interval time.Duration) {
	if interval > 0 {
		q.heartbeatInterval = interval
	}
}

// EnqueueServiceDeployment pushes a service deployment to the Redis queue using strictly typed payload.
func (q *DeploymentQueue) EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error {
	job := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  serviceDeploymentID,
		ServiceDeploymentID: serviceDeploymentID,
	}
	err := q.client.LPush(ctx, DeploymentQueueKey, job.Encode()).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue service deployment %s: %w", serviceDeploymentID, err)
	}
	slog.Info("enqueued service deployment job", "service_deployment_id", serviceDeploymentID)
	return nil
}

// EnqueueDeployment pushes a deployment / release ID to the Redis queue using strictly typed payload.
func (q *DeploymentQueue) EnqueueDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	job := Job{
		Type:         JobTypeDeployment,
		ID:           deploymentID,
		DeploymentID: &deploymentID,
	}
	err := q.client.LPush(ctx, DeploymentQueueKey, job.Encode()).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue deployment %s: %w", deploymentID, err)
	}
	slog.Info("enqueued release deployment job", "deployment_id", deploymentID)
	return nil
}

// acquireLease attempts to acquire an exclusive execution lease for a job.
func (q *DeploymentQueue) acquireLease(ctx context.Context, jobID uuid.UUID, workerID string) (bool, error) {
	lockKey := JobProcessingPrefix + jobID.String()
	leaseKey := JobLeasePrefix + jobID.String()

	acquired, err := q.client.SetNX(ctx, lockKey, workerID, q.leaseTTL).Result()
	if err != nil {
		return false, err
	}
	if !acquired {
		// If lock exists, check if lease exists and is still valid
		leaseExists, _ := q.client.Exists(ctx, leaseKey).Result()
		if leaseExists > 0 {
			return false, nil
		}
		// Previous worker lease expired without releasing lock. Take over the orphaned lock.
		q.client.Del(ctx, lockKey)
		acquired, err = q.client.SetNX(ctx, lockKey, workerID, q.leaseTTL).Result()
		if err != nil || !acquired {
			return false, err
		}
	}

	now := time.Now().Format(time.RFC3339)
	pipe := q.client.Pipeline()
	pipe.HSet(ctx, leaseKey, "worker_id", workerID, "acquired_at", now, "renewed_at", now)
	pipe.Expire(ctx, leaseKey, q.leaseTTL)
	_, err = pipe.Exec(ctx)
	if err != nil {
		q.client.Del(ctx, lockKey)
		return false, err
	}

	return true, nil
}

// extendLease renews the TTL of the worker's processing lock and lease.
func (q *DeploymentQueue) extendLease(ctx context.Context, jobID uuid.UUID) error {
	lockKey := JobProcessingPrefix + jobID.String()
	leaseKey := JobLeasePrefix + jobID.String()

	pipe := q.client.Pipeline()
	pipe.Expire(ctx, lockKey, q.leaseTTL)
	pipe.HSet(ctx, leaseKey, "renewed_at", time.Now().Format(time.RFC3339))
	pipe.Expire(ctx, leaseKey, q.leaseTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// releaseLease deletes the lease and processing lock for a completed or aborted job.
func (q *DeploymentQueue) releaseLease(ctx context.Context, jobID uuid.UUID) {
	lockKey := JobProcessingPrefix + jobID.String()
	leaseKey := JobLeasePrefix + jobID.String()
	q.client.Del(ctx, lockKey, leaseKey)
}

// HasActiveLease returns true if an active, non-expired worker lease exists for the job.
func (q *DeploymentQueue) HasActiveLease(ctx context.Context, jobID uuid.UUID) (bool, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	n, err := q.client.Exists(ctx, leaseKey).Result()
	return n > 0, err
}

// IsJobInQueue checks if the job is waiting in the pending queue.
func (q *DeploymentQueue) IsJobInQueue(ctx context.Context, jobID uuid.UUID) (bool, error) {
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

// IsJobInProcessing checks if the job is recorded in the processing queue.
func (q *DeploymentQueue) IsJobInProcessing(ctx context.Context, jobID uuid.UUID) (bool, error) {
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

// RecoverAbandonedJobs inspects jobs remaining in the processing queue.
// If a job's lease has expired (e.g. worker process crashed), it checks whether
// the deployment is already terminal. If not terminal and attempts < maxRetries,
// it re-enqueues the job instead of waiting and failing.
func (q *DeploymentQueue) RecoverAbandonedJobs(ctx context.Context) (int, error) {
	items, err := q.client.LRange(ctx, ProcessingQueueKey, 0, -1).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to inspect processing queue: %w", err)
	}

	recovered := 0
	for _, item := range items {
		job, err := ParseJob(item)
		if err != nil {
			// Corrupt / untyped payload in processing queue -> move to DLQ
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeadLetterQueueKey, item)
			continue
		}

		keyID := job.ID.String()
		leaseKey := JobLeasePrefix + keyID

		// 1. Check if worker still holds an active lease
		hasLease, err := q.client.Exists(ctx, leaseKey).Result()
		if err == nil && hasLease > 0 {
			// Worker is alive and heartbeating. Not abandoned.
			continue
		}

		// 2. Worker is dead or abandoned. Check if job already reached terminal state.
		if q.terminalChecker != nil {
			checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
			term, termErr := q.terminalChecker(checkCtx, job)
			checkCancel()
			if termErr == nil && term {
				slog.Info("abandoned job is already in terminal state; discarding without retry",
					"job_id", job.ID, "type", job.Type)
				q.client.LRem(ctx, ProcessingQueueKey, 1, item)
				q.releaseLease(ctx, job.ID)
				continue
			}
		}

		// 3. Check attempt count
		attemptsStr, _ := q.client.Get(ctx, JobAttemptsPrefix+keyID).Result()
		attempts, _ := strconv.Atoi(attemptsStr)

		if attempts >= q.maxRetries {
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeadLetterQueueKey, item)
			q.releaseLease(ctx, job.ID)
			slog.Warn("abandoned job exceeded max retries, moved to DLQ", "job_id", job.ID, "attempts", attempts)
		} else {
			// Increment attempt on recovery to bound recovery cycles
			q.client.Incr(ctx, JobAttemptsPrefix+keyID)
			q.client.Expire(ctx, JobAttemptsPrefix+keyID, 24*time.Hour)

			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.releaseLease(ctx, job.ID)
			q.client.LPush(ctx, DeploymentQueueKey, item)
			slog.Info("re-enqueued recoverable orphaned deployment job with expired lease",
				"job_id", job.ID, "attempts", attempts+1)
			recovered++
		}
	}

	return recovered, nil
}

// StartJobWorker starts background worker goroutines to consume deployment jobs reliably.
func (q *DeploymentQueue) StartJobWorker(parentCtx context.Context, handler func(ctx context.Context, job Job) error) {
	workerCtx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if recCount, err := q.RecoverAbandonedJobs(recoverCtx); err == nil && recCount > 0 {
		slog.Info("recovered abandoned deployment jobs on startup", "count", recCount)
	}
	recoverCancel()

	workerCount := q.workerCount
	if workerCount <= 0 {
		workerCount = DefaultWorkerCount
	}

	for i := 0; i < workerCount; i++ {
		workerIdx := i + 1
		q.wg.Add(1)
		go func(workerID int) {
			defer q.wg.Done()
			workerName := fmt.Sprintf("worker-%d-%s", workerID, uuid.New().String()[:8])
			slog.Info("deployment queue worker started", "worker", workerName)

			for {
				select {
				case <-workerCtx.Done():
					slog.Info("deployment queue worker stopping", "worker", workerName)
					return
				default:
					itemStr, err := q.client.BRPopLPush(workerCtx, DeploymentQueueKey, ProcessingQueueKey, 2*time.Second).Result()
					if err != nil {
						if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
							continue
						}
						slog.Error("redis queue pop error", "worker", workerName, "error", err)
						time.Sleep(1 * time.Second)
						continue
					}

					if itemStr == "" {
						continue
					}

					job, err := ParseJob(itemStr)
					if err != nil {
						slog.Error("invalid job payload in queue, moving to DLQ", "val", itemStr, "error", err)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						_ = q.client.LPush(context.Background(), DeadLetterQueueKey, itemStr)
						continue
					}

					// Acquire execution lease with heartbeat tracking
					acquired, err := q.acquireLease(workerCtx, job.ID, workerName)
					if err != nil {
						slog.Error("failed to check job idempotency / acquire lease", "job_id", job.ID, "error", err)
						continue
					}
					if !acquired {
						slog.Warn("skipping duplicate deployment job with active lease", "job_id", job.ID)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						continue
					}

					// Heartbeat ticker to continuously extend the lease during long builds/deploys
					hbCtx, cancelHb := context.WithCancel(workerCtx)
					go func() {
						ticker := time.NewTicker(q.heartbeatInterval)
						defer ticker.Stop()
						for {
							select {
							case <-hbCtx.Done():
								return
							case <-ticker.C:
								_ = q.extendLease(context.Background(), job.ID)
							}
						}
					}()

					slog.Info("processing deployment job", "worker", workerName, "job_id", job.ID, "job_type", job.Type)
					execErr := handler(workerCtx, job)

					// Stop heartbeat and cleanup lease
					cancelHb()
					q.releaseLease(context.Background(), job.ID)

					keyID := job.ID.String()
					if execErr != nil {
						attempts, _ := q.client.Incr(context.Background(), JobAttemptsPrefix+keyID).Result()
						q.client.Expire(context.Background(), JobAttemptsPrefix+keyID, 24*time.Hour)

						slog.Error("deployment job execution failed",
							"job_id", job.ID,
							"job_type", job.Type,
							"error", execErr,
							"attempt", attempts,
							"max_retries", q.maxRetries,
						)

						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)

						// Check if job is in a terminal state: NEVER blindly retry a terminal ServiceDeployment!
						isTerminal := false
						if q.terminalChecker != nil {
							checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
							term, err := q.terminalChecker(checkCtx, job)
							checkCancel()
							if err == nil && term {
								isTerminal = true
							}
						}

						if !isTerminal && int(attempts) < q.maxRetries {
							slog.Warn("re-queueing failed deployment for retry",
								"job_id", job.ID,
								"attempt", attempts,
								"backoff", q.retryBackoff,
							)
							time.Sleep(q.retryBackoff)
							_ = q.client.LPush(context.Background(), DeploymentQueueKey, itemStr)
						} else {
							if isTerminal {
								slog.Info("deployment job is already in terminal state; skipping retry",
									"job_id", job.ID,
									"attempts", attempts,
								)
							} else {
								slog.Error("deployment job exceeded max retry attempts; moving to dead-letter queue",
									"job_id", job.ID,
									"attempts", attempts,
								)
							}
							_ = q.client.LPush(context.Background(), DeadLetterQueueKey, itemStr)
						}
					} else {
						slog.Info("deployment job completed successfully", "worker", workerName, "job_id", job.ID)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						q.client.Del(context.Background(), JobAttemptsPrefix+keyID)
					}
				}
			}
		}(workerIdx)
	}
}

// StartWorker is the backward-compatible entrypoint that adapts an ID-based handler.
func (q *DeploymentQueue) StartWorker(parentCtx context.Context, handler func(ctx context.Context, deploymentID uuid.UUID) error) {
	q.StartJobWorker(parentCtx, func(ctx context.Context, job Job) error {
		return handler(ctx, job.ID)
	})
}

// Stop gracefully shuts down the worker goroutines.
func (q *DeploymentQueue) Stop() {
	if q.cancel != nil {
		q.cancel()
	}
	q.wg.Wait()
	slog.Info("deployment queue worker shut down complete")
}

// GetQueueStats returns the count of pending, in-flight, and dead-letter jobs.
func (q *DeploymentQueue) GetQueueStats(ctx context.Context) (pending, processing, deadLetter int64, err error) {
	pending, err = q.client.LLen(ctx, DeploymentQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	processing, err = q.client.LLen(ctx, ProcessingQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	deadLetter, err = q.client.LLen(ctx, DeadLetterQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	return pending, processing, deadLetter, nil
}
