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
	DeploymentQueueKey  = "forgelab:queue:deployments"
	ProcessingQueueKey  = "forgelab:queue:deployments:processing"
	DeadLetterQueueKey  = "forgelab:queue:deployments:dead_letter"
	JobProcessingPrefix = "forgelab:job:processing:"
	JobAttemptsPrefix   = "forgelab:job:attempts:"
	DefaultMaxRetries   = 3
	DefaultRetryBackoff = 3 * time.Second
	DefaultWorkerCount  = 4
)

var (
	ErrJobAlreadyProcessing = errors.New("deployment job is already being processed")
)

type JobType string

const (
	JobTypeServiceDeployment JobType = "service_deployment"
	JobTypeDeployment        JobType = "deployment"
)

// Job represents a queued deployment unit.
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

// ParseJob decodes a queue item string into a Job.
func ParseJob(item string) (Job, error) {
	trimmed := strings.TrimSpace(item)
	if strings.HasPrefix(trimmed, "{") {
		var j Job
		if err := json.Unmarshal([]byte(trimmed), &j); err == nil {
			if j.ID == uuid.Nil && j.ServiceDeploymentID != uuid.Nil {
				j.ID = j.ServiceDeploymentID
			}
			return j, nil
		}
	}

	// Plain UUID fallback
	id, err := uuid.Parse(trimmed)
	if err != nil {
		return Job{}, fmt.Errorf("invalid job payload: %w", err)
	}

	return Job{
		Type:                JobTypeServiceDeployment,
		ID:                  id,
		ServiceDeploymentID: id,
	}, nil
}

type DeploymentQueue struct {
	client      *redis.Client
	maxRetries  int
	workerCount int
	wg          sync.WaitGroup
	cancel      context.CancelFunc
}

func NewDeploymentQueue(client *redis.Client) *DeploymentQueue {
	return &DeploymentQueue{
		client:      client,
		maxRetries:  DefaultMaxRetries,
		workerCount: DefaultWorkerCount,
	}
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

// EnqueueServiceDeployment pushes a service deployment to the Redis queue.
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

// EnqueueDeployment pushes a deployment / release ID to the Redis queue.
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

// RecoverAbandonedJobs checks for jobs that remained in the processing queue due to worker crashes.
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
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			continue
		}

		keyID := job.ID.String()
		attemptsStr, _ := q.client.Get(ctx, JobAttemptsPrefix+keyID).Result()
		attempts, _ := strconv.Atoi(attemptsStr)

		if attempts >= q.maxRetries {
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeadLetterQueueKey, item)
			slog.Warn("abandoned job exceeded max retries, moved to DLQ", "job_id", job.ID, "attempts", attempts)
		} else {
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeploymentQueueKey, item)
			q.client.Del(ctx, JobProcessingPrefix+keyID)
			slog.Info("recovered abandoned deployment job from previous crash", "job_id", job.ID, "attempts", attempts)
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
			slog.Info("deployment queue worker started", "worker_index", workerID)

			for {
				select {
				case <-workerCtx.Done():
					slog.Info("deployment queue worker stopping", "worker_index", workerID)
					return
				default:
					itemStr, err := q.client.BRPopLPush(workerCtx, DeploymentQueueKey, ProcessingQueueKey, 2*time.Second).Result()
					if err != nil {
						if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
							continue
						}
						slog.Error("redis queue pop error", "worker_index", workerID, "error", err)
						time.Sleep(1 * time.Second)
						continue
					}

					if itemStr == "" {
						continue
					}

					job, err := ParseJob(itemStr)
					if err != nil {
						slog.Error("invalid job payload in queue, discarding", "val", itemStr, "error", err)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						continue
					}

					lockKey := JobProcessingPrefix + job.ID.String()
					acquired, err := q.client.SetNX(workerCtx, lockKey, "1", 30*time.Minute).Result()
					if err != nil {
						slog.Error("failed to check job idempotency lock", "job_id", job.ID, "error", err)
						continue
					}
					if !acquired {
						slog.Warn("skipping duplicate deployment job", "job_id", job.ID)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						continue
					}

					slog.Info("processing deployment job", "worker_index", workerID, "job_id", job.ID, "job_type", job.Type)
					execErr := handler(workerCtx, job)

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
						q.client.Del(context.Background(), lockKey)

						if int(attempts) < q.maxRetries {
							slog.Warn("re-queueing failed deployment for retry",
								"job_id", job.ID,
								"attempt", attempts,
								"backoff", DefaultRetryBackoff,
							)
							time.Sleep(DefaultRetryBackoff)
							_ = q.client.LPush(context.Background(), DeploymentQueueKey, itemStr)
						} else {
							slog.Error("deployment job exceeded max retry attempts; moving to dead-letter queue",
								"job_id", job.ID,
								"attempts", attempts,
							)
							_ = q.client.LPush(context.Background(), DeadLetterQueueKey, itemStr)
						}
					} else {
						slog.Info("deployment job completed successfully", "worker_index", workerID, "job_id", job.ID)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						q.client.Del(context.Background(), JobAttemptsPrefix+keyID)
						q.client.Del(context.Background(), lockKey)
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

// Stop gracefully shuts down the worker.
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
