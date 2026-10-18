package docker

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LogCollectorInfo captures runtime metrics and state of a single log collector.
type LogCollectorInfo struct {
	ID                  string    `json:"id"`
	ContainerID         string    `json:"container_id"`
	DeploymentID        string    `json:"deployment_id,omitempty"`
	ServiceDeploymentID string    `json:"service_deployment_id,omitempty"`
	ServiceID           string    `json:"service_id,omitempty"`
	ServiceName         string    `json:"service_name,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	LastActivity        time.Time `json:"last_activity"`
	LinesCollected      int64     `json:"lines_collected"`
	Retries             int       `json:"retries"`
	CollectionErrors    int64     `json:"collection_errors"`
	PersistenceErrors   int64     `json:"persistence_errors"`
	LastError           string    `json:"last_error,omitempty"`
}

// LogCollectorDiagnostics provides operational observability into all active log collectors.
type LogCollectorDiagnostics struct {
	ActiveCollectors       int                `json:"active_collectors"`
	TotalLinesCollected    int64              `json:"total_lines_collected"`
	TotalCollectionErrors  int64              `json:"total_collection_errors"`
	TotalPersistenceErrors int64              `json:"total_persistence_errors"`
	Collectors             []LogCollectorInfo `json:"collectors"`
}

type logCollectorRegistration struct {
	info   LogCollectorInfo
	cancel context.CancelFunc
	keys   []string
}

// LogCollectorRegistry provides race-safe registration, cleanup, and diagnostics
// for all running Docker runtime log collectors. It prevents an older collector
// from accidentally deregistering a replacement collector during container redeployments.
type LogCollectorRegistry struct {
	mu                     sync.RWMutex
	byID                   map[string]*logCollectorRegistration
	byKey                  map[string]string // logicalKey -> instanceID
	totalLinesCollected    int64
	totalCollectionErrors  int64
	totalPersistenceErrors int64
}

// NewLogCollectorRegistry creates an empty collector registry.
func NewLogCollectorRegistry() *LogCollectorRegistry {
	return &LogCollectorRegistry{
		byID:  make(map[string]*logCollectorRegistration),
		byKey: make(map[string]string),
	}
}

// sanitizeError removes potential secrets, tokens, or sensitive substrings from error messages.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 256 {
		msg = msg[:256] + "..."
	}
	// Sanitize possible authorization tokens or passwords
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
		return "internal collector error (details sanitized for security)"
	}
	return msg
}

// Register registers a new log collector instance, cancelling any older collector
// mapped to the same keys. Returns a unique instanceID.
func (r *LogCollectorRegistry) Register(p RuntimeCollectorParams, cancel context.CancelFunc, keys ...string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	instanceID := uuid.New().String()

	// Cancel and remove previous active collectors for any overlapping keys
	cancelledOldIDs := make(map[string]bool)
	for _, k := range keys {
		if oldID, exists := r.byKey[k]; exists {
			cancelledOldIDs[oldID] = true
		}
	}
	for oldID := range cancelledOldIDs {
		if oldReg, ok := r.byID[oldID]; ok {
			if oldReg.cancel != nil {
				oldReg.cancel()
			}
			for _, oldKey := range oldReg.keys {
				if r.byKey[oldKey] == oldID {
					delete(r.byKey, oldKey)
				}
			}
			delete(r.byID, oldID)
		}
	}

	for _, k := range keys {
		r.byKey[k] = instanceID
	}

	cid := p.ContainerID
	if len(cid) > 12 {
		cid = cid[:12]
	}

	info := LogCollectorInfo{
		ID:           instanceID,
		ContainerID:  cid,
		ServiceName:  p.ServiceName,
		StartedAt:    time.Now().UTC(),
		LastActivity: time.Now().UTC(),
	}
	if p.DeploymentID != nil {
		info.DeploymentID = p.DeploymentID.String()
	}
	if p.ServiceDeploymentID != nil {
		info.ServiceDeploymentID = p.ServiceDeploymentID.String()
	}
	if p.ServiceID != nil {
		info.ServiceID = p.ServiceID.String()
	}

	r.byID[instanceID] = &logCollectorRegistration{
		info:   info,
		cancel: cancel,
		keys:   keys,
	}

	return instanceID
}

// Unregister safely removes a collector instance. Crucially, it only removes
// logical key mappings if they still point to THIS specific instanceID, preventing
// an old terminating collector from deregistering a newer replacement collector.
func (r *LogCollectorRegistry) Unregister(instanceID string, keys ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, k := range keys {
		if currentID, exists := r.byKey[k]; exists && currentID == instanceID {
			delete(r.byKey, k)
		}
	}

	if reg, ok := r.byID[instanceID]; ok {
		if reg.cancel != nil {
			reg.cancel()
		}
		delete(r.byID, instanceID)
	}
}

// Stop cancels and removes the collector associated with the given key.
func (r *LogCollectorRegistry) Stop(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	instanceID, exists := r.byKey[key]
	if !exists {
		return
	}

	if reg, ok := r.byID[instanceID]; ok {
		if reg.cancel != nil {
			reg.cancel()
		}
		for _, k := range reg.keys {
			if r.byKey[k] == instanceID {
				delete(r.byKey, k)
			}
		}
		delete(r.byID, instanceID)
	}
	delete(r.byKey, key)
}

// StopAll cancels and removes all active log collectors (used on shutdown).
func (r *LogCollectorRegistry) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, reg := range r.byID {
		if reg.cancel != nil {
			reg.cancel()
		}
	}

	r.byID = make(map[string]*logCollectorRegistration)
	r.byKey = make(map[string]string)
}

// IsActive returns whether a collector is actively running for the given key.
func (r *LogCollectorRegistry) IsActive(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	instanceID, exists := r.byKey[key]
	if !exists {
		return false
	}
	_, active := r.byID[instanceID]
	return active
}

// RecordActivity notes that lines were successfully collected by this instance.
func (r *LogCollectorRegistry) RecordActivity(instanceID string, lineCount int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalLinesCollected += lineCount
	if reg, ok := r.byID[instanceID]; ok {
		reg.info.LinesCollected += lineCount
		reg.info.LastActivity = time.Now().UTC()
	}
}

// RecordCollectionError notes a stream read or attach failure for this instance.
func (r *LogCollectorRegistry) RecordCollectionError(instanceID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalCollectionErrors++
	if reg, ok := r.byID[instanceID]; ok {
		reg.info.CollectionErrors++
		reg.info.Retries++
		reg.info.LastError = sanitizeError(err)
	}
}

// RecordPersistenceError notes a database persistence failure for this instance.
func (r *LogCollectorRegistry) RecordPersistenceError(instanceID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalPersistenceErrors++
	if reg, ok := r.byID[instanceID]; ok {
		reg.info.PersistenceErrors++
		reg.info.LastError = sanitizeError(err)
	}
}

// GetDiagnostics returns a point-in-time snapshot of collector health and metrics.
func (r *LogCollectorRegistry) GetDiagnostics() LogCollectorDiagnostics {
	r.mu.RLock()
	defer r.mu.RUnlock()

	collectors := make([]LogCollectorInfo, 0, len(r.byID))
	for _, reg := range r.byID {
		collectors = append(collectors, reg.info)
	}

	return LogCollectorDiagnostics{
		ActiveCollectors:       len(r.byID),
		TotalLinesCollected:    r.totalLinesCollected,
		TotalCollectionErrors:  r.totalCollectionErrors,
		TotalPersistenceErrors: r.totalPersistenceErrors,
		Collectors:             collectors,
	}
}
