package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
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
