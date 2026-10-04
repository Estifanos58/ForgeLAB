package docker

import (
	"context"
	"sync"
)

// BuildSemaphore provides a dynamic, thread-safe concurrency limiter
// specifically for Docker image builds. It supports dynamic limit adjustments
// without leaking semaphore slots, desynchronizing channels, or deadlocking.
type BuildSemaphore struct {
	mu      sync.Mutex
	limit   int
	active  int
	waiters []chan struct{}
}

// NewBuildSemaphore initializes a build semaphore with the given limit.
func NewBuildSemaphore(limit int) *BuildSemaphore {
	if limit <= 0 {
		limit = 1
	}
	return &BuildSemaphore{
		limit: limit,
	}
}

// SetLimit dynamically changes the concurrency limit.
// If the limit is increased, waiting builds are immediately notified up to the new limit.
// If decreased, existing active builds complete normally without being aborted.
func (s *BuildSemaphore) SetLimit(newLimit int) {
	if newLimit <= 0 {
		newLimit = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.limit = newLimit

	// Wake up any waiting callers if slots are now available
	for s.active < s.limit && len(s.waiters) > 0 {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.active++
		close(w)
	}
}

// GetLimit returns the configured concurrency limit.
func (s *BuildSemaphore) GetLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

// GetActiveCount returns the current number of actively running builds.
func (s *BuildSemaphore) GetActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Acquire requests a build slot, waiting if necessary until one becomes available or ctx is cancelled.
// It returns a release function that must be called when the build finishes.
func (s *BuildSemaphore) Acquire(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.active < s.limit {
		s.active++
		s.mu.Unlock()

		var once sync.Once
		return func() {
			once.Do(func() {
				s.release()
			})
		}, nil
	}

	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	s.mu.Unlock()

	select {
	case <-ch:
		var once sync.Once
		return func() {
			once.Do(func() {
				s.release()
			})
		}, nil
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-ch:
			// Slot was already granted right as cancellation occurred; must release it
			s.mu.Unlock()
			s.release()
			return nil, ctx.Err()
		default:
			// Remove ch from waiters list
			for i, w := range s.waiters {
				if w == ch {
					s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

func (s *BuildSemaphore) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active--
	if s.active < s.limit && len(s.waiters) > 0 {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.active++
		close(w)
	}
}
