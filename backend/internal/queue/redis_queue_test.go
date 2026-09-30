package queue

import (
	"context"
	"errors"
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
	pending, processing, dlq, err := q.GetQueueStats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if pending != 0 || processing != 0 || dlq != 0 {
		t.Errorf("expected all queues empty, got pending=%d, processing=%d, dlq=%d", pending, processing, dlq)
	}
}

func TestDeploymentQueue_CrashRecovery(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	crashedID := uuid.New()

	ctx := context.Background()

	// Simulate a job left in processing queue due to a sudden crash
	err := client.LPush(ctx, ProcessingQueueKey, crashedID.String()).Err()
	if err != nil {
		t.Fatalf("failed to seed processing queue: %v", err)
	}

	// Verify it's in processing
	_, processing, _, _ := q.GetQueueStats(ctx)
	if processing != 1 {
		t.Fatalf("expected 1 job in processing, got %d", processing)
	}

	// Run crash recovery sweep
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
}

func TestDeploymentQueue_RetryAndDeadLetterQueue(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	q := NewDeploymentQueue(client)
	q.SetMaxRetries(2) // 2 attempts max

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
					// Wait briefly for DLQ push
					for i := 0; i < 20; i++ {
						time.Sleep(100 * time.Millisecond)
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

	// Enqueue failing job
	if err := q.EnqueueDeployment(ctx, failingID); err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	select {
	case <-dlqDone:
	case <-time.After(10 * time.Second):
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
