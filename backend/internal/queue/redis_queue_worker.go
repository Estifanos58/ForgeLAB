package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StartJobWorker starts background worker goroutines to consume deployment jobs reliably.
func (q *DeploymentQueue) StartJobWorker(parentCtx context.Context, handler func(ctx context.Context, job Job) error) {
	workerCtx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if recCount, err := q.RecoverAbandonedJobs(recoverCtx); err == nil && recCount > 0 {
		slog.Info("recovered abandoned deployment jobs on startup", "count", recCount)
	}
	recoverCancel()

	// Launch periodic abandoned-job recovery to periodically re-enqueue stranded/expired jobs
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				recCtx, recCancel := context.WithTimeout(workerCtx, 10*time.Second)
				recCount, err := q.RecoverAbandonedJobs(recCtx)
				recCancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("periodic abandoned job recovery encountered error", "error", err)
				} else if recCount > 0 {
					slog.Info("periodic recovery processed abandoned deployment jobs", "count", recCount)
				}
			}
		}
	}()

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
						select {
						case <-workerCtx.Done():
							return
						case <-time.After(1 * time.Second):
						}
						continue
					}

					if itemStr == "" {
						continue
					}

					job, err := ParseJob(itemStr)
					if err != nil {
						slog.Error("invalid job payload in queue, moving to DLQ", "val", itemStr, "error", err)
						// Atomic LREM + LPUSH to prevent double-move by concurrent workers
						_, _ = corruptToDLQScript.Run(context.Background(), q.client,
							[]string{ProcessingQueueKey, DeadLetterQueueKey},
							itemStr,
						).Result()
						continue
					}

					// Acquire execution lease with unique ownership token.
					// acquireLease atomically sets both the lease key and state to "processing".
					leaseToken, acquired, err := q.acquireLease(workerCtx, job.ID, workerName)
					if err != nil {
						slog.Error("failed to check job idempotency / acquire lease", "job_id", job.ID, "error", err)

						canProceed := "1"
						if workerCtx.Err() != nil {
							canProceed = "0"
						}

						// Reconcile lease acquisition error atomically to prevent:
						// 1. Dropping active jobs if lease was never acquired or expired.
						// 2. Duplicate executions if another worker already holds the lease.
						// 3. Incorrect state resets of active jobs.
						reconCode, reconErr := q.ReconcileLeaseError(context.Background(), job.ID, leaseToken, itemStr, canProceed)
						if reconErr != nil {
							slog.Error("failed to reconcile lease error with redis; leaving for recovery loop", "job_id", job.ID, "error", reconErr)
							time.Sleep(500 * time.Millisecond)
							continue
						}

						if reconCode == 1 {
							// Lease was actually acquired on Redis despite client error, and worker can proceed.
							slog.Info("reconciled ambiguous lease acquisition: worker owns lease, proceeding", "job_id", job.ID, "token", leaseToken)
							acquired = true
						} else {
							// Handled safely (requeued, aborted, or duplicate removed without resetting active state).
							select {
							case <-workerCtx.Done():
								return
							case <-time.After(500 * time.Millisecond):
							}
							continue
						}
					}
					if !acquired {
						// Another worker owns this job. Remove our redundant copy from processing queue
						// only if lease is still active. If lease expired in the interim, restores to deployment queue.
						slog.Warn("skipping duplicate deployment job with active lease", "job_id", job.ID)
						if _, dupErr := q.RemoveDuplicateIfLeaseHeld(context.Background(), job.ID, itemStr); dupErr != nil {
							slog.Error("failed to remove duplicate job cleanly", "job_id", job.ID, "error", dupErr)
						}
						continue
					}

					// Context for this specific job execution: cancelled if workerCtx terminates OR if lease is lost!
					jobCtx, cancelJob := context.WithCancel(workerCtx)

					// Heartbeat ticker to continuously extend the lease during long builds/deploys
					// and promptly abort execution if job was cancelled or superseded
					hbCtx, cancelHb := context.WithCancel(workerCtx)
					go func() {
						ticker := time.NewTicker(q.heartbeatInterval)
						defer ticker.Stop()
						for {
							select {
							case <-hbCtx.Done():
								return
							case <-ticker.C:
								// Check if deployment was cancelled or became terminal in DB
								if q.terminalChecker != nil {
									checkCtx, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
									term, checkErr := q.terminalChecker(checkCtx, job)
									checkCancel()
									if checkErr == nil && term {
										slog.Info("job became terminal or cancelled; aborting execution",
											"job_id", job.ID)
										cancelJob()
										return
									}
								}

								if err := q.extendLease(context.Background(), job.ID, leaseToken); err != nil {
									slog.Warn("failed to extend lease, canceling job execution",
										"job_id", job.ID, "error", err)
									cancelJob()
									return
								}
							}
						}
					}()

					slog.Info("processing deployment job", "worker", workerName, "job_id", job.ID, "job_type", job.Type)
					execErr := handler(jobCtx, job)

					// Stop heartbeat ticker immediately
					cancelHb()
					cancelJob()

					// Atomic lease-fenced mutations:
					// Every state change (requeue, DLQ, finalization, terminal cleanup) is an atomic
					// Lua script that verifies lease ownership before performing any mutation.
					// If the worker lost its lease to another worker or expiration, the script safely aborts (no-op).
					if execErr != nil {
						slog.Error("deployment job execution failed",
							"job_id", job.ID,
							"job_type", job.Type,
							"error", execErr,
						)

						// Check if job is in a terminal state: NEVER retry a terminal ServiceDeployment!
						isTerminal := false
						if q.terminalChecker != nil {
							checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
							term, err := q.terminalChecker(checkCtx, job)
							checkCancel()
							if err == nil && term {
								isTerminal = true
							}
						}

						if isTerminal {
							slog.Info("deployment job is already in terminal state; performing atomic terminal cleanup",
								"job_id", job.ID, "worker", workerName)
							ok, err := q.TerminalCleanup(context.Background(), job.ID, leaseToken, itemStr, true)
							if err != nil || !ok {
								slog.Warn("worker lost lease; skipped terminal cleanup to protect replacement worker",
									"worker", workerName, "job_id", job.ID, "err", err)
							}
						} else {
							// Check current attempts to decide whether next attempt exceeds maxRetries
							attempts, _ := q.GetAttempts(context.Background(), job.ID)
							if attempts+1 < q.maxRetries {
								slog.Warn("re-queueing failed deployment for retry",
									"job_id", job.ID,
									"current_attempts", attempts,
									"backoff", q.retryBackoff,
								)
								select {
								case <-workerCtx.Done():
									slog.Info("worker context cancelled during retry backoff; immediately re-queueing without blocking", "job_id", job.ID)
									_, _, _ = q.FailAndRequeue(context.Background(), job.ID, leaseToken, itemStr)
									return
								case <-time.After(q.retryBackoff):
								}
								newAtt, ok, err := q.FailAndRequeue(context.Background(), job.ID, leaseToken, itemStr)
								if err != nil || !ok {
									slog.Warn("worker lost lease; aborted requeue to protect replacement worker",
										"worker", workerName, "job_id", job.ID, "err", err)
								} else {
									atomic.AddInt64(&q.totalRetryAttempts, 1)
									slog.Info("re-queued failed deployment job", "job_id", job.ID, "new_attempts", newAtt)
								}
							} else {
								slog.Error("deployment job exceeded max retry attempts; moving to dead-letter queue",
									"job_id", job.ID,
									"current_attempts", attempts,
								)
								newAtt, ok, err := q.FailToDLQ(context.Background(), job.ID, leaseToken, itemStr)
								if err != nil || !ok {
									slog.Warn("worker lost lease; aborted DLQ transfer to protect replacement worker",
										"worker", workerName, "job_id", job.ID, "err", err)
								} else {
									atomic.AddInt64(&q.totalDeadLetterJobs, 1)
									slog.Info("transferred failed deployment to DLQ", "job_id", job.ID, "final_attempts", newAtt)
								}
							}
						}
					} else {
						slog.Info("deployment job completed successfully; performing atomic finalization",
							"worker", workerName, "job_id", job.ID)
						ok, err := q.FinalizeSuccess(context.Background(), job.ID, leaseToken, itemStr)
						if err != nil || !ok {
							slog.Warn("worker lost lease; aborted finalization to protect replacement worker",
								"worker", workerName, "job_id", job.ID, "err", err)
						}
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
