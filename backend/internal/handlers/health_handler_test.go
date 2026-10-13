package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/queue"
	"github.com/redis/go-redis/v9"
)

func TestHealthHandler_Liveness(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()

	h.Liveness(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestHealthHandler_Readiness_ReportsFailuresWithoutSecrets(t *testing.T) {
	// With uninitialized dependencies, readiness must return 503 and report down status
	h := NewHealthHandler(nil, nil, nil)
	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()

	h.Readiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
	}

	var resp ReadinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if resp.Status != "not_ready" {
		t.Errorf("expected not_ready, got %s", resp.Status)
	}

	// Verify all dependencies reported down
	if resp.Dependencies["postgres"].Status != "down" {
		t.Errorf("expected postgres down, got %s", resp.Dependencies["postgres"].Status)
	}
	if resp.Dependencies["redis"].Status != "down" {
		t.Errorf("expected redis down, got %s", resp.Dependencies["redis"].Status)
	}
	if resp.Dependencies["docker"].Status != "down" {
		t.Errorf("expected docker down, got %s", resp.Dependencies["docker"].Status)
	}

	// Verify no secrets or sensitive info in response
	rawJSON := rec.Body.String()
	sensitiveTerms := []string{"password", "postgres://", "redis://", "secret", "token"}
	for _, term := range sensitiveTerms {
		if containsSubstring(rawJSON, term) {
			t.Errorf("found sensitive term %q in readiness response: %s", term, rawJSON)
		}
	}
}

func TestHealthHandler_Readiness_PartialSuccess(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis error: %v", err)
	}
	defer mr.Close()

	redisCli := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisCli.Close()

	// Redis is up, postgres and docker are nil
	h := NewHealthHandler(nil, redisCli, nil)
	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()

	h.Readiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp ReadinessResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Dependencies["redis"].Status != "up" {
		t.Errorf("expected redis up, got %s", resp.Dependencies["redis"].Status)
	}
	if resp.Dependencies["postgres"].Status != "down" {
		t.Errorf("expected postgres down, got %s", resp.Dependencies["postgres"].Status)
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type mockQueueProvider struct {
	diag queue.QueueDiagnostics
	err  error
}

func (m *mockQueueProvider) GetQueueDiagnostics(ctx context.Context) (queue.QueueDiagnostics, error) {
	return m.diag, m.err
}

func TestHealthHandler_QueueDiagnostics(t *testing.T) {
	// 1. Without provider -> 501 Not Implemented
	h := NewHealthHandler(nil, nil, nil)
	req := httptest.NewRequest("GET", "/health/queue", nil)
	rec := httptest.NewRecorder()
	h.QueueDiagnostics(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 Not Implemented without provider, got %d", rec.Code)
	}

	// 2. With provider returning error -> 503 Service Unavailable
	mock := &mockQueueProvider{
		err: errors.New("redis unavailable"),
	}
	h.SetQueueDiagnosticsProvider(mock)
	rec = httptest.NewRecorder()
	h.QueueDiagnostics(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on provider error, got %d", rec.Code)
	}

	// 3. With provider returning stats -> 200 OK
	mock.err = nil
	mock.diag = queue.QueueDiagnostics{
		PendingJobs:        5,
		ProcessingJobs:     2,
		DeadLetterJobs:     0,
		TotalRecoveredJobs: 1,
	}
	rec = httptest.NewRecorder()
	h.QueueDiagnostics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var diag queue.QueueDiagnostics
	if err := json.Unmarshal(rec.Body.Bytes(), &diag); err != nil {
		t.Fatalf("failed to unmarshal diag: %v", err)
	}
	if diag.PendingJobs != 5 || diag.TotalRecoveredJobs != 1 {
		t.Errorf("unexpected diag content: %+v", diag)
	}
}

func TestHealthHandler_Readiness_WithQueueDiagnostics(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil)
	h.SetQueueDiagnosticsProvider(&mockQueueProvider{
		diag: queue.QueueDiagnostics{
			PendingJobs:        3,
			ProcessingJobs:     1,
			TotalRecoveredJobs: 4,
		},
	})

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Readiness(rec, req)

	var resp ReadinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal readiness response: %v", err)
	}

	if resp.Queue == nil {
		t.Fatalf("expected queue diagnostics in readiness response, got nil")
	}
	if resp.Queue.PendingJobs != 3 || resp.Queue.TotalRecoveredJobs != 4 {
		t.Errorf("unexpected queue metrics in readiness response: %+v", resp.Queue)
	}
}

type mockBuildQueueProvider struct {
	diag docker.SharedBuildDiagnostics
}

func (m *mockBuildQueueProvider) GetSharedBuildDiagnostics() docker.SharedBuildDiagnostics {
	return m.diag
}

func TestHealthHandler_BuildQueueDiagnostics(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil)
	buildData := docker.SharedBuildDiagnostics{
		TotalEntries: 2,
		ActiveBuilds: 1,
	}
	h.SetBuildQueueDiagnosticsProvider(&mockBuildQueueProvider{diag: buildData})

	req := httptest.NewRequest("GET", "/health/queue", nil)
	rec := httptest.NewRecorder()
	h.QueueDiagnostics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with build queue provider, got %d", rec.Code)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to unmarshal queue diagnostics: %v", err)
	}
	if raw["build_queue"] == nil {
		t.Errorf("expected build_queue in response, got %v", raw)
	}
}
