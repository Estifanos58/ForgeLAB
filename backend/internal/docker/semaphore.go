package docker

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultMaxConcurrentBuilds calculates conservative, CPU-aware build concurrency.
func DefaultMaxConcurrentBuilds() int {
	cpus := runtime.NumCPU()
	if cpus <= 2 {
		return 1
	}
	if cpus <= 4 {
		return 2
	}
	n := cpus / 2
	if n > 4 {
		return 4
	}
	return n
}

// SlotOwnerInfo captures rich, non-sensitive diagnostic metadata for an
// active or queued build-slot participant.
type SlotOwnerInfo struct {
	ID                  string    `json:"id"`
	DeploymentID        string    `json:"deployment_id,omitempty"`
	ServiceDeploymentID string    `json:"service_deployment_id,omitempty"`
	ServiceName         string    `json:"service_name,omitempty"`
	Phase               string    `json:"phase"` // "queued", "waiting_for_build_slot", "context_generation", "context_transfer", "context_validation", "docker_build", "slot_granted"
	EnteredQueueAt      time.Time `json:"entered_queue_at"`
	AcquiredAt          time.Time `json:"acquired_at,omitempty"`
}

// SemaphoreDiagnostics provides a structured snapshot of current build slot capacity and waiters.
type SemaphoreDiagnostics struct {
	Limit        int             `json:"limit"`
	ActiveCount  int             `json:"active_count"`
	WaitersCount int             `json:"waiters_count"`
	ActiveSlots  []SlotOwnerInfo `json:"active_slots"`
	Waiters      []SlotOwnerInfo `json:"waiters"`
}

type waiterItem struct {
	ch   chan struct{}
	info SlotOwnerInfo
}

// BuildSemaphore provides a dynamic, thread-safe concurrency limiter
// specifically for Docker image builds with first-class ownership diagnostics,
// queue progress observability, and cancellation resilience.
type BuildSemaphore struct {
	mu          sync.Mutex
	limit       int
	active      int
	activeSlots map[string]*SlotOwnerInfo
	waiters     []*waiterItem
}

// NewBuildSemaphore initializes a build semaphore with the given limit.
func NewBuildSemaphore(limit int) *BuildSemaphore {
	if limit <= 0 {
		limit = 1
	}
	return &BuildSemaphore{
		limit:       limit,
		activeSlots: make(map[string]*SlotOwnerInfo),
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
		w.info.AcquiredAt = time.Now()
		w.info.Phase = "slot_granted"
		s.activeSlots[w.info.ID] = &w.info
		close(w.ch)
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

// GetWaitersCount returns the current number of builds waiting for a slot.
func (s *BuildSemaphore) GetWaitersCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiters)
}

// GetDiagnostics returns a full diagnostic snapshot of active slots and queued waiters.
func (s *BuildSemaphore) GetDiagnostics() SemaphoreDiagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()

	active := make([]SlotOwnerInfo, 0, len(s.activeSlots))
	for _, v := range s.activeSlots {
		if v != nil {
			active = append(active, *v)
		}
	}

	waiters := make([]SlotOwnerInfo, 0, len(s.waiters))
	for _, w := range s.waiters {
		if w != nil {
			waiters = append(waiters, w.info)
		}
	}

	return SemaphoreDiagnostics{
		Limit:        s.limit,
		ActiveCount:  s.active,
		WaitersCount: len(s.waiters),
		ActiveSlots:  active,
		Waiters:      waiters,
	}
}

// UpdatePhase updates the current execution phase of an active slot holder.
func (s *BuildSemaphore) UpdatePhase(slotID, newPhase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slot, exists := s.activeSlots[slotID]; exists && slot != nil {
		slot.Phase = newPhase
	}
}

// Acquire requests a build slot, waiting if necessary until one becomes available or ctx is cancelled.
// It returns a release function that must be called when the build finishes.
func (s *BuildSemaphore) Acquire(ctx context.Context) (func(), error) {
	return s.AcquireWithInfo(ctx, SlotOwnerInfo{
		ID:    uuid.NewString(),
		Phase: "building",
	}, nil)
}

// AcquireWithInfo requests a build slot with detailed owner metadata and queue wait progress callbacks.
func (s *BuildSemaphore) AcquireWithInfo(
	ctx context.Context,
	info SlotOwnerInfo,
	onWait func(position int, waitDuration time.Duration, activeSlots []SlotOwnerInfo),
) (func(), error) {
	if info.ID == "" {
		info.ID = uuid.NewString()
	}
	now := time.Now()
	info.EnteredQueueAt = now

	s.mu.Lock()
	if s.active < s.limit {
		s.active++
		info.AcquiredAt = now
		info.Phase = "slot_granted"
		s.activeSlots[info.ID] = &info
		s.mu.Unlock()

		var once sync.Once
		return func() {
			once.Do(func() {
				s.release(info.ID)
			})
		}, nil
	}

	// Queue the waiter
	info.Phase = "waiting_for_build_slot"
	item := &waiterItem{
		ch:   make(chan struct{}),
		info: info,
	}
	s.waiters = append(s.waiters, item)
	queuePosition := len(s.waiters)

	// Collect active owners for initial progress reporting
	activeCopy := make([]SlotOwnerInfo, 0, len(s.activeSlots))
	for _, v := range s.activeSlots {
		if v != nil {
			activeCopy = append(activeCopy, *v)
		}
	}
	s.mu.Unlock()

	if onWait != nil {
		onWait(queuePosition, 0, activeCopy)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-item.ch:
			var once sync.Once
			return func() {
				once.Do(func() {
					s.release(info.ID)
				})
			}, nil

		case tickTime := <-ticker.C:
			if onWait != nil {
				s.mu.Lock()
				pos := -1
				for idx, w := range s.waiters {
					if w == item {
						pos = idx + 1
						break
					}
				}
				actCopy := make([]SlotOwnerInfo, 0, len(s.activeSlots))
				for _, v := range s.activeSlots {
					if v != nil {
						actCopy = append(actCopy, *v)
					}
				}
				s.mu.Unlock()
				if pos > 0 {
					onWait(pos, tickTime.Sub(now), actCopy)
				}
			}

		case <-ctx.Done():
			s.mu.Lock()
			select {
			case <-item.ch:
				// Slot was granted right as cancellation arrived; release it cleanly
				s.mu.Unlock()
				s.release(info.ID)
				return nil, ctx.Err()
			default:
				// Remove item from waiters
				for i, w := range s.waiters {
					if w == item {
						s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
						break
					}
				}
				s.mu.Unlock()
				return nil, ctx.Err()
			}
		}
	}
}

func (s *BuildSemaphore) release(slotID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.activeSlots, slotID)
	s.active--

	if s.active < s.limit && len(s.waiters) > 0 {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.active++
		w.info.AcquiredAt = time.Now()
		w.info.Phase = "slot_granted"
		s.activeSlots[w.info.ID] = &w.info
		close(w.ch)
	}
}
