package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SharedBuildPromise coordinates multiple concurrent services that share an identical
// build context, Dockerfile, and build configuration.
type SharedBuildPromise struct {
	done           chan struct{}
	once           sync.Once
	mu             sync.RWMutex
	imageTag       string
	digest         string
	err            error
	phase          string // "waiting", "building", "succeeded", "failed", "cancelled"
	startedAt      time.Time
	finishedAt     time.Time
	ownerServiceID string
}

// NewSharedBuildPromise initializes a promise for a shared build.
func NewSharedBuildPromise(ownerServiceID string) *SharedBuildPromise {
	return &SharedBuildPromise{
		done:           make(chan struct{}),
		phase:          "building",
		startedAt:      time.Now(),
		ownerServiceID: ownerServiceID,
	}
}

// Done returns the channel that closes when the build completes, fails, or is cancelled.
func (p *SharedBuildPromise) Done() <-chan struct{} {
	return p.done
}

// IsDone returns true if the promise channel has been closed.
func (p *SharedBuildPromise) IsDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Result returns the outcome of the shared build.
func (p *SharedBuildPromise) Result() (string, string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.imageTag, p.digest, p.err
}

// Complete marks the shared build as successfully finished and wakes all waiting services.
func (p *SharedBuildPromise) Complete(imageTag, digest string) {
	p.once.Do(func() {
		p.mu.Lock()
		p.imageTag = imageTag
		p.digest = digest
		p.phase = "succeeded"
		p.finishedAt = time.Now()
		p.mu.Unlock()

		select {
		case <-p.done:
		default:
			close(p.done)
		}
	})
}

// Fail marks the shared build as failed and notifies all waiting services.
func (p *SharedBuildPromise) Fail(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		if err == nil {
			err = errors.New("shared build aborted without specific error")
		}
		p.err = err
		p.phase = "failed"
		p.finishedAt = time.Now()
		p.mu.Unlock()

		select {
		case <-p.done:
		default:
			close(p.done)
		}
	})
}

// SharedBuildDiagnostics captures runtime snapshot of shared builds.
type SharedBuildDiagnostics struct {
	TotalEntries int                       `json:"total_entries"`
	ActiveBuilds int                       `json:"active_builds"`
	Entries      map[string]SharedItemInfo `json:"entries"`
}

// SharedItemInfo contains diagnostic info for a single shared build artifact entry.
type SharedItemInfo struct {
	Key            string        `json:"key"`
	Phase          string        `json:"phase"`
	ImageTag       string        `json:"image_tag,omitempty"`
	Digest         string        `json:"digest,omitempty"`
	OwnerServiceID string        `json:"owner_service_id,omitempty"`
	Duration       time.Duration `json:"duration,omitempty"`
	HasError       bool          `json:"has_error"`
	ErrorMessage   string        `json:"error_message,omitempty"`
}

// SharedBuildCoordinator manages the lifecycle of shared build promises.
type SharedBuildCoordinator struct {
	mu       sync.RWMutex
	promises map[string]*SharedBuildPromise
}

// NewSharedBuildCoordinator initializes a new coordinator.
func NewSharedBuildCoordinator() *SharedBuildCoordinator {
	return &SharedBuildCoordinator{
		promises: make(map[string]*SharedBuildPromise),
	}
}

// GetOrInit retrieves an existing promise or registers a new one if none exists.
// Returns the promise and true if it already existed (follower), or false if newly registered (leader).
func (c *SharedBuildCoordinator) GetOrInit(key string, ownerServiceID string) (*SharedBuildPromise, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if p, exists := c.promises[key]; exists {
		p.mu.RLock()
		failed := p.err != nil
		p.mu.RUnlock()
		if !failed {
			return p, true
		}
		// Discard previously failed promise so retry attempts can proceed cleanly
		delete(c.promises, key)
	}

	p := NewSharedBuildPromise(ownerServiceID)
	c.promises[key] = p
	return p, false
}

// Get retrieves an existing promise by key.
func (c *SharedBuildCoordinator) Get(key string) (*SharedBuildPromise, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, exists := c.promises[key]
	return p, exists
}

// Complete completes a promise by key.
func (c *SharedBuildCoordinator) Complete(key, imageTag, digest string) {
	c.mu.RLock()
	p, exists := c.promises[key]
	c.mu.RUnlock()

	if exists && p != nil {
		p.Complete(imageTag, digest)
	}
}

// Fail fails a promise by key.
func (c *SharedBuildCoordinator) Fail(key string, err error) {
	c.mu.RLock()
	p, exists := c.promises[key]
	c.mu.RUnlock()

	if exists && p != nil {
		p.Fail(err)
	}
}

// Remove deletes an entry from the coordinator.
func (c *SharedBuildCoordinator) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.promises, key)
}

// CleanupDeployment removes all completed entries associated with a project/deployment prefix.
func (c *SharedBuildCoordinator) CleanupDeployment(projectID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	cleaned := 0
	prefix := projectID + ":"
	for k, p := range c.promises {
		if strings.HasPrefix(k, prefix) && p.IsDone() {
			delete(c.promises, k)
			cleaned++
		}
	}
	return cleaned
}

// GetDiagnostics returns a full snapshot of in-flight and completed shared builds.
func (c *SharedBuildCoordinator) GetDiagnostics() SharedBuildDiagnostics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	diag := SharedBuildDiagnostics{
		TotalEntries: len(c.promises),
		Entries:      make(map[string]SharedItemInfo, len(c.promises)),
	}

	for k, p := range c.promises {
		p.mu.RLock()
		dur := time.Since(p.startedAt)
		if !p.finishedAt.IsZero() {
			dur = p.finishedAt.Sub(p.startedAt)
		}
		errMsg := ""
		if p.err != nil {
			errMsg = p.err.Error()
		}
		if p.phase == "building" {
			diag.ActiveBuilds++
		}
		diag.Entries[k] = SharedItemInfo{
			Key:            k,
			Phase:          p.phase,
			ImageTag:       p.imageTag,
			Digest:         p.digest,
			OwnerServiceID: p.ownerServiceID,
			Duration:       dur,
			HasError:       p.err != nil,
			ErrorMessage:   errMsg,
		}
		p.mu.RUnlock()
	}

	return diag
}

// ComputeSharedBuildKey calculates a deterministic cache key that incorporates ALL
// configuration affecting the generated Docker image artifact.
func ComputeSharedBuildKey(
	projectID string,
	sourceRevision string,
	buildContext string,
	dockerfilePath string,
	buildStrategy string,
	targetStage string,
	buildArgs map[string]*string,
	runtimeType string,
	internalPort int,
	startCommand string,
	packageManager string,
) string {
	cleanCtx := filepath.ToSlash(filepath.Clean(filepath.FromSlash(buildContext)))
	if cleanCtx == "" {
		cleanCtx = "."
	}
	cleanDF := filepath.ToSlash(filepath.Clean(filepath.FromSlash(dockerfilePath)))
	if cleanDF == "" || cleanDF == "." {
		cleanDF = "Dockerfile"
	}

	// Canonicalize build args
	var argPairs []string
	if len(buildArgs) > 0 {
		keys := make([]string, 0, len(buildArgs))
		for k := range buildArgs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := ""
			if buildArgs[k] != nil {
				val = *buildArgs[k]
			}
			argPairs = append(argPairs, fmt.Sprintf("%s=%s", k, val))
		}
	}
	argStr := strings.Join(argPairs, ",")

	// Build raw representation
	rawConfig := fmt.Sprintf("strategy=%s|target=%s|args=%s|rt=%s|port=%d|start=%s|pm=%s",
		buildStrategy,
		targetStage,
		argStr,
		runtimeType,
		internalPort,
		startCommand,
		packageManager,
	)

	h := sha256.Sum256([]byte(rawConfig))
	cfgHash := hex.EncodeToString(h[:8]) // 16-char hex prefix

	return fmt.Sprintf("%s:%s:%s:%s:%s",
		projectID,
		sourceRevision,
		cleanCtx,
		cleanDF,
		cfgHash,
	)
}
