package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	DeploymentQueueKey = "forgelab:queue:deployments"
	JobProcessingPrefix = "forgelab:job:processing:"
)

var (
	ErrJobAlreadyProcessing = errors.New("deployment job is already being processed")
)

type DeploymentQueue struct {
	client *redis.Client
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func NewDeploymentQueue(client *redis.Client) *DeploymentQueue {
	return &DeploymentQueue{
		client: client,
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

// StartWorker starts background worker goroutines to consume deployment jobs.
func (q *DeploymentQueue) StartWorker(parentCtx context.Context, handler func(ctx context.Context, deploymentID uuid.UUID) error) {
	workerCtx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		slog.Info("deployment queue worker started")

		for {
			select {
			case <-workerCtx.Done():
				slog.Info("deployment queue worker stopping")
				return
			default:
				// BRPop blocks for 2 seconds waiting for a job
				res, err := q.client.BRPop(workerCtx, 2*time.Second, DeploymentQueueKey).Result()
				if err != nil {
					if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
						continue
					}
					slog.Error("redis queue pop error", "error", err)
					time.Sleep(1 * time.Second)
					continue
				}

				if len(res) < 2 {
					continue
				}

				deploymentIDStr := res[1]
				deploymentID, err := uuid.Parse(deploymentIDStr)
				if err != nil {
					slog.Error("invalid deployment ID in queue", "val", deploymentIDStr, "error", err)
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
					continue
				}

				slog.Info("processing deployment job", "deployment_id", deploymentID)
				if err := handler(workerCtx, deploymentID); err != nil {
					slog.Error("deployment job execution failed", "deployment_id", deploymentID, "error", err)
				} else {
					slog.Info("deployment job completed successfully", "deployment_id", deploymentID)
				}

				// Remove lock when done
				q.client.Del(context.Background(), lockKey)
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
