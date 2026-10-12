package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/forgelab/backend/internal/queue"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type QueueDiagnosticsProvider interface {
	GetQueueDiagnostics(ctx context.Context) (queue.QueueDiagnostics, error)
}

type DependencyStatus struct {
	Status    string `json:"status"`               // "up" or "down"
	LatencyMs *int64 `json:"latency_ms,omitempty"` // round-trip ping time
	Error     string `json:"error,omitempty"`      // sanitized error message if down
}

type ReadinessResponse struct {
	Status       string                      `json:"status"` // "ready" or "not_ready"
	Timestamp    string                      `json:"timestamp"`
	Dependencies map[string]DependencyStatus `json:"dependencies"`
	Queue        *queue.QueueDiagnostics     `json:"queue,omitempty"`
}

type HealthHandler struct {
	db            *pgxpool.Pool
	redisClient   *redis.Client
	dockerClient  *client.Client
	queueProvider QueueDiagnosticsProvider
}

func NewHealthHandler(db *pgxpool.Pool, redisClient *redis.Client, dockerClient *client.Client) *HealthHandler {
	return &HealthHandler{
		db:           db,
		redisClient:  redisClient,
		dockerClient: dockerClient,
	}
}

func (h *HealthHandler) SetQueueDiagnosticsProvider(provider QueueDiagnosticsProvider) {
	h.queueProvider = provider
}

// Liveness handles GET /health (lightweight liveness probe)
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status": "ok", "service": "forgelab"}`))
}

// Readiness handles GET /ready and GET /health/ready (verifies PostgreSQL, Redis, and Docker Engine)
func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	deps := make(map[string]DependencyStatus)
	var mu sync.Mutex
	var wg sync.WaitGroup

	allReady := true

	// 1. PostgreSQL check
	wg.Add(1)
	go func() {
		defer wg.Done()
		dep := DependencyStatus{}
		if h.db == nil {
			dep.Status = "down"
			dep.Error = "database connection pool is uninitialized"
		} else {
			start := time.Now()
			err := h.db.Ping(ctx)
			latency := time.Since(start).Milliseconds()
			if err != nil {
				dep.Status = "down"
				dep.Error = "database ping failed"
			} else {
				dep.Status = "up"
				dep.LatencyMs = &latency
			}
		}

		mu.Lock()
		deps["postgres"] = dep
		if dep.Status != "up" {
			allReady = false
		}
		mu.Unlock()
	}()

	// 2. Redis check
	wg.Add(1)
	go func() {
		defer wg.Done()
		dep := DependencyStatus{}
		if h.redisClient == nil {
			dep.Status = "down"
			dep.Error = "redis client is uninitialized"
		} else {
			start := time.Now()
			_, err := h.redisClient.Ping(ctx).Result()
			latency := time.Since(start).Milliseconds()
			if err != nil {
				dep.Status = "down"
				dep.Error = "redis ping failed"
			} else {
				dep.Status = "up"
				dep.LatencyMs = &latency
			}
		}

		mu.Lock()
		deps["redis"] = dep
		if dep.Status != "up" {
			allReady = false
		}
		mu.Unlock()
	}()

	// 3. Docker Engine check
	wg.Add(1)
	go func() {
		defer wg.Done()
		dep := DependencyStatus{}
		if h.dockerClient == nil {
			dep.Status = "down"
			dep.Error = "docker engine client is uninitialized"
		} else {
			start := time.Now()
			_, err := h.dockerClient.Ping(ctx)
			latency := time.Since(start).Milliseconds()
			if err != nil {
				dep.Status = "down"
				dep.Error = "docker engine daemon is unreachable"
			} else {
				dep.Status = "up"
				dep.LatencyMs = &latency
			}
		}

		mu.Lock()
		deps["docker"] = dep
		if dep.Status != "up" {
			allReady = false
		}
		mu.Unlock()
	}()

	wg.Wait()

	var queueDiag *queue.QueueDiagnostics
	if h.queueProvider != nil {
		if qd, err := h.queueProvider.GetQueueDiagnostics(ctx); err == nil {
			queueDiag = &qd
		}
	}

	overallStatus := "ready"
	httpStatus := http.StatusOK
	if !allReady {
		overallStatus = "not_ready"
		httpStatus = http.StatusServiceUnavailable
	}

	resp := ReadinessResponse{
		Status:       overallStatus,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Dependencies: deps,
		Queue:        queueDiag,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(resp)
}

// QueueDiagnostics handles GET /health/queue (queue metrics and recovery statistics)
func (h *HealthHandler) QueueDiagnostics(w http.ResponseWriter, r *http.Request) {
	if h.queueProvider == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "queue diagnostics provider not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	diag, err := h.queueProvider.GetQueueDiagnostics(ctx)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to retrieve queue diagnostics"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(diag)
}
