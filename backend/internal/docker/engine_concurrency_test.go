package docker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEngine_GlobalConcurrencyLimit(t *testing.T) {
	e := &Engine{}
	e.SetMaxConcurrentBuilds(2)

	if e.GetMaxConcurrentBuilds() != 2 {
		t.Fatalf("expected max concurrent builds 2, got %d", e.GetMaxConcurrentBuilds())
	}

	ctx := context.Background()

	// Acquire slot 1
	release1, err := e.acquireBuildSlot(ctx)
	if err != nil {
		t.Fatalf("failed to acquire slot 1: %v", err)
	}

	// Acquire slot 2
	release2, err := e.acquireBuildSlot(ctx)
	if err != nil {
		t.Fatalf("failed to acquire slot 2: %v", err)
	}

	// Slot 3 must block because capacity is 2
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = e.acquireBuildSlot(timeoutCtx)
	if err == nil {
		t.Fatalf("expected acquire slot 3 to time out, but it succeeded")
	}

	// Release slot 1
	release1()

	// Now slot 3 must succeed
	acquireCtx, acquireCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer acquireCancel()

	release3, err := e.acquireBuildSlot(acquireCtx)
	if err != nil {
		t.Fatalf("failed to acquire slot 3 after releasing slot 1: %v", err)
	}
	release3()
	release2()
}

func TestEngine_HighConcurrencyContention(t *testing.T) {
	e := &Engine{}
	const limit = 3
	e.SetMaxConcurrentBuilds(limit)

	const totalWorkers = 15
	var activeCount int32
	var maxActive int32
	var mu sync.Mutex
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < totalWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := e.acquireBuildSlot(ctx)
			if err != nil {
				t.Errorf("acquire build slot error: %v", err)
				return
			}
			curr := atomic.AddInt32(&activeCount, 1)
			mu.Lock()
			if curr > maxActive {
				maxActive = curr
			}
			mu.Unlock()

			// simulate brief build work
			time.Sleep(20 * time.Millisecond)

			atomic.AddInt32(&activeCount, -1)
			release()
		}()
	}

	wg.Wait()

	mu.Lock()
	peak := maxActive
	mu.Unlock()

	if peak > limit {
		t.Fatalf("concurrency limit breached: peak active was %d, limit is %d", peak, limit)
	}
	if peak == 0 {
		t.Fatalf("expected some workers to run, got 0")
	}
}
