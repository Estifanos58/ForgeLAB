package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RecoverJobAtomic executes the atomic recovery script for an abandoned or orphaned job.
// Returns 0: not recovered (lease exists or not in processing queue), 1: re-enqueued, 2: DLQ, 3: terminal cleanup.
func (q *DeploymentQueue) RecoverJobAtomic(ctx context.Context, itemStr string, jobID uuid.UUID, maxRetries int, isTerminal bool) (int, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	mode := "retry"
	if isTerminal {
		mode = "terminal"
	}

	res, err := atomicRecoverScript.Run(ctx, q.client,
		[]string{ProcessingQueueKey, DeploymentQueueKey, leaseKey, attemptsKey, stateKey, DeadLetterQueueKey},
		itemStr, mode, strconv.Itoa(maxRetries),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to execute atomic recovery script: %w", err)
	}
	code, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected return from atomic recovery script: %v", res)
	}
	return int(code), nil
}

// ReconcileLeaseError atomically reconciles the state of a job when acquireLease encounters an ambiguous error.
// Returns:
//
//	1: caller holds the lease and can proceed
//	2: caller held the lease, aborted, released lease and requeued
//	3: another worker holds active lease; duplicate removed from processing queue only (state and deployment queue untouched)
//	4: no active lease found; stranded job safely requeued to deployment queue with pending state
func (q *DeploymentQueue) ReconcileLeaseError(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr, canProceed string) (int, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := reconcileLeaseErrorScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeploymentQueueKey, stateKey},
		leaseToken, itemStr, canProceed,
	).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to execute reconcile lease error script: %w", err)
	}
	code, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected return from reconcile lease error script: %v", res)
	}
	return int(code), nil
}

// RemoveDuplicateIfLeaseHeld removes a redundant job entry from processing queue only if an active lease is held.
// If the lease expired in the interim, it safely restores the job to the deployment queue so it cannot be lost.
// Returns:
//
//	1: duplicate removed while active lease held
//	2: lease expired; restored to deployment queue
func (q *DeploymentQueue) RemoveDuplicateIfLeaseHeld(ctx context.Context, jobID uuid.UUID, itemStr string) (int, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := removeDuplicateIfLeaseHeldScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeploymentQueueKey, stateKey},
		itemStr,
	).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to execute remove duplicate script: %w", err)
	}
	code, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected return from remove duplicate script: %v", res)
	}
	return int(code), nil
}

// RecoverAbandonedJobs inspects jobs remaining in the processing queue.
// If a job's lease has expired (e.g. worker process crashed), it checks whether
// the deployment is already terminal. If not terminal and attempts < maxRetries,
// it atomically re-enqueues the job instead of waiting and failing.
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
			// Corrupt / untyped payload in processing queue -> atomically move to DLQ
			_, _ = corruptToDLQScript.Run(ctx, q.client,
				[]string{ProcessingQueueKey, DeadLetterQueueKey},
				item,
			).Result()
			continue
		}

		// Check if job already reached terminal state
		isTerminal := false
		if q.terminalChecker != nil {
			checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
			term, termErr := q.terminalChecker(checkCtx, job)
			checkCancel()
			if termErr == nil && term {
				isTerminal = true
			}
		}

		// Atomically check lease absence, check/increment attempts, and move job
		res, recErr := q.RecoverJobAtomic(ctx, item, job.ID, q.maxRetries, isTerminal)
		if recErr != nil {
			slog.Error("failed to recover abandoned job", "job_id", job.ID, "error", recErr)
			continue
		}
		if res == 1 {
			slog.Info("re-enqueued recoverable orphaned deployment job with expired lease",
				"job_id", job.ID)
			recovered++
		} else if res == 2 {
			slog.Warn("abandoned job exceeded max retries, moved to DLQ", "job_id", job.ID)
		} else if res == 3 {
			slog.Info("abandoned job is already in terminal state; cleaned up from processing queue",
				"job_id", job.ID, "type", job.Type)
		}
	}

	if recovered > 0 {
		atomic.AddInt64(&q.totalRecoveredJobs, int64(recovered))
	}

	return recovered, nil
}
