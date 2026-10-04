package docker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSemaphore_BasicAcquireRelease(t *testing.T) {
	sem := NewBuildSemaphore(2)
	assert.Equal(t, 2, sem.GetLimit())
	assert.Equal(t, 0, sem.GetActiveCount())

	ctx := context.Background()

	rel1, err := sem.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sem.GetActiveCount())

	rel2, err := sem.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, sem.GetActiveCount())

	// Slot 3 times out
	toCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = sem.Acquire(toCtx)
	require.Error(t, err)
	assert.Equal(t, 2, sem.GetActiveCount())

	// Release one
	rel1()
	assert.Equal(t, 1, sem.GetActiveCount())

	// Calling release again is a no-op (once.Do)
	rel1()
	assert.Equal(t, 1, sem.GetActiveCount())

	rel2()
	assert.Equal(t, 0, sem.GetActiveCount())
}

func TestBuildSemaphore_DynamicResize_Increase(t *testing.T) {
	sem := NewBuildSemaphore(1)
	ctx := context.Background()

	rel1, err := sem.Acquire(ctx)
	require.NoError(t, err)

	started := make(chan struct{})
	acquired := atomic.Bool{}

	go func() {
		close(started)
		rel2, err := sem.Acquire(ctx)
		if err == nil {
			acquired.Store(true)
			rel2()
		}
	}()

	<-started
	time.Sleep(30 * time.Millisecond)
	assert.False(t, acquired.Load(), "waiter must be blocked while limit is 1")

	// Dynamically increase limit from 1 to 2
	sem.SetLimit(2)
	assert.Equal(t, 2, sem.GetLimit())

	// Wait for the waiter to acquire
	require.Eventually(t, func() bool {
		return acquired.Load()
	}, 1*time.Second, 10*time.Millisecond)

	rel1()
	assert.Equal(t, 0, sem.GetActiveCount())
}

func TestBuildSemaphore_DynamicResize_Decrease(t *testing.T) {
	sem := NewBuildSemaphore(3)
	ctx := context.Background()

	rel1, _ := sem.Acquire(ctx)
	rel2, _ := sem.Acquire(ctx)
	rel3, _ := sem.Acquire(ctx)
	assert.Equal(t, 3, sem.GetActiveCount())

	// Decrease limit to 1 while 3 are active
	sem.SetLimit(1)
	assert.Equal(t, 1, sem.GetLimit())

	// Active count remains 3 until released
	assert.Equal(t, 3, sem.GetActiveCount())

	rel1()
	assert.Equal(t, 2, sem.GetActiveCount())

	rel2()
	assert.Equal(t, 1, sem.GetActiveCount())

	rel3()
	assert.Equal(t, 0, sem.GetActiveCount())

	// Now a new acquire can grab the 1 slot
	rel4, err := sem.Acquire(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sem.GetActiveCount())
	rel4()
}

func TestBuildSemaphore_ConcurrentStress_NoLeaks(t *testing.T) {
	const limit = 2
	sem := NewBuildSemaphore(limit)

	var wg sync.WaitGroup
	var activeCount int32
	var maxObserved int32
	var mu sync.Mutex

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := sem.Acquire(ctx)
			if err != nil {
				return
			}
			curr := atomic.AddInt32(&activeCount, 1)
			mu.Lock()
			if curr > maxObserved {
				maxObserved = curr
			}
			mu.Unlock()

			time.Sleep(10 * time.Millisecond)

			atomic.AddInt32(&activeCount, -1)
			rel()
		}()
	}

	wg.Wait()

	assert.LessOrEqual(t, maxObserved, int32(limit), "concurrency limit must never be exceeded")
	assert.Equal(t, 0, sem.GetActiveCount(), "all slots must be released; zero leaks")
}
