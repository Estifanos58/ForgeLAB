package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
)

var (
	ErrJobAlreadyProcessing = errors.New("deployment job is already being processed")
)

type DeploymentQueue struct {
	client     *redis.Client
	maxRetries int
	wg         sync.WaitGroup
	cancel     context.CancelFunc
}

func NewDeploymentQueue(client *redis.Client) *DeploymentQueue {
	return &DeploymentQueue{
		client:     client,
		maxRetries: DefaultMaxRetries,
	}
}

// SetMaxRetries configures the maximum retry attempts for a failing deployment job.
func (q *DeploymentQueue) SetMaxRetries(n int) {
	if n > 0 {
		q.maxRetries = n
	}
}

// EnqueueDeployment pushes a deployment ID to the Redis queue.
func (q *DeploymentQueue) EnqueueDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	err := q.client.LPush(ctx, DeploymentQueueKey, deploymentID.String()).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue deployment %s: %w", deploymentID, err)
	}
	slog.Info("enqueued deployment job", "deployment_id", deploymentID)
	return nil
}

// RecoverAbandonedJobs checks for jobs that remained in the processing queue due to worker crashes.
func (q *DeploymentQueue) RecoverAbandonedJobs(ctx context.Context) (int, error) {
	// Retrieve all jobs currently in the processing queue
	items, err := q.client.LRange(ctx, ProcessingQueueKey, 0, -1).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to inspect processing queue: %w", err)
	}

	recovered := 0
	for _, item := range items {
		depID, err := uuid.Parse(item)
		if err != nil {
			// Malformed item, prune it
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			continue
		}

		// Check attempt count
		attemptsStr, _ := q.client.Get(ctx, JobAttemptsPrefix+item).Result()
		attempts, _ := strconv.Atoi(attemptsStr)

		if attempts >= q.maxRetries {
			// Exceeded max retries: move to dead letter queue
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeadLetterQueueKey, item)
			slog.Warn("abandoned job exceeded max retries, moved to DLQ", "deployment_id", depID, "attempts", attempts)
		} else {
			// Re-enqueue into main queue for retry
			q.client.LRem(ctx, ProcessingQueueKey, 1, item)
			q.client.LPush(ctx, DeploymentQueueKey, item)
			// Clear stale processing lock
			q.client.Del(ctx, JobProcessingPrefix+item)
			slog.Info("recovered abandoned deployment job from previous crash", "deployment_id", depID, "attempts", attempts)
			recovered++
		}
	}

	return recovered, nil
}

// StartWorker starts background worker goroutines to consume deployment jobs reliably.
func (q *DeploymentQueue) StartWorker(parentCtx context.Context, handler func(ctx context.Context, deploymentID uuid.UUID) error) {
	workerCtx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	// Recover any abandoned jobs from previous crashes before accepting new work
	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if recCount, err := q.RecoverAbandonedJobs(recoverCtx); err == nil && recCount > 0 {
		slog.Info("recovered abandoned deployment jobs on startup", "count", recCount)
	}
	recoverCancel()

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		slog.Info("deployment queue worker started with crash-safe delivery & retry handling")

		for {
			select {
			case <-workerCtx.Done():
				slog.Info("deployment queue worker stopping")
				return
			default:
				// BRPopLPush atomically moves an element from DeploymentQueueKey to ProcessingQueueKey
				// preventing job loss if the worker crashes mid-flight.
				deploymentIDStr, err := q.client.BRPopLPush(workerCtx, DeploymentQueueKey, ProcessingQueueKey, 2*time.Second).Result()
				if err != nil {
					if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
						continue
					}
					slog.Error("redis queue pop error", "error", err)
					time.Sleep(1 * time.Second)
					continue
				}

				if deploymentIDStr == "" {
					continue
				}

				deploymentID, err := uuid.Parse(deploymentIDStr)
				if err != nil {
					slog.Error("invalid deployment ID in queue, discarding", "val", deploymentIDStr, "error", err)
					q.client.LRem(context.Background(), ProcessingQueueKey, 1, deploymentIDStr)
					continue
				}

				// Idempotency / duplicate protection using SETNX
				lockKey := JobProcessingPrefix + deploymentIDStr
				acquired, err := q.client.SetNX(workerCtx, lockKey, "1", 30*time.Minute).Result()
				if err != nil {
					slog.Error("failed to check job idempotency lock", "deployment_id", deploymentID, "error", err)
					continue
				}
				if !acquired {
					slog.Warn("skipping duplicate deployment job", "deployment_id", deploymentID)
					// Remove from processing since duplicate is already being handled
					q.client.LRem(context.Background(), ProcessingQueueKey, 1, deploymentIDStr)
					continue
				}

				slog.Info("processing deployment job", "deployment_id", deploymentID)
				execErr := handler(workerCtx, deploymentID)

				if execErr != nil {
					// Increment attempt counter
					attempts, _ := q.client.Incr(context.Background(), JobAttemptsPrefix+deploymentIDStr).Result()
					q.client.Expire(context.Background(), JobAttemptsPrefix+deploymentIDStr, 24*time.Hour)

					slog.Error("deployment job execution failed",
						"deployment_id", deploymentID,
						"error", execErr,
						"attempt", attempts,
						"max_retries", q.maxRetries,
					)

					// Remove from processing queue
					q.client.LRem(context.Background(), ProcessingQueueKey, 1, deploymentIDStr)
					// Release idempotency lock
					q.client.Del(context.Background(), lockKey)

					if int(attempts) < q.maxRetries {
						slog.Warn("re-queueing failed deployment for retry",
							"deployment_id", deploymentID,
							"attempt", attempts,
							"backoff", DefaultRetryBackoff,
						)
						time.Sleep(DefaultRetryBackoff)
						// Re-enqueue for retry
						_ = q.client.LPush(context.Background(), DeploymentQueueKey, deploymentIDStr)
					} else {
						slog.Error("deployment job exceeded max retry attempts; moving to dead-letter queue",
							"deployment_id", deploymentID,
							"attempts", attempts,
						)
						// Push to dead letter queue
						_ = q.client.LPush(context.Background(), DeadLetterQueueKey, deploymentIDStr)
					}
				} else {
					slog.Info("deployment job completed successfully", "deployment_id", deploymentID)
					// Clean up from processing queue, attempts counter, and idempotency lock
					q.client.LRem(context.Background(), ProcessingQueueKey, 1, deploymentIDStr)
					q.client.Del(context.Background(), JobAttemptsPrefix+deploymentIDStr)
					q.client.Del(context.Background(), lockKey)
				}
			}
		}
	}()
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
