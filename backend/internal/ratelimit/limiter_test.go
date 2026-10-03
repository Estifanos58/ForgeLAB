package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/middleware"
)

func TestLimiter_InMemoryFallback(t *testing.T) {
	limiter := NewLimiter(nil, true)
	ctx := context.Background()

	// Limit 3 per second
	for i := 0; i < 3; i++ {
		allowed, _, err := limiter.Allow(ctx, "test", "user-1", 3, 1*time.Second)
		if err != nil || !allowed {
			t.Fatalf("expected allowed on iteration %d, got allowed=%v, err=%v", i, allowed, err)
		}
	}

	// 4th request must be rejected
	allowed, retryAfter, err := limiter.Allow(ctx, "test", "user-1", 3, 1*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatalf("expected 4th request to be blocked")
	}
	if retryAfter <= 0 {
		t.Fatalf("expected positive retryAfter duration, got %v", retryAfter)
	}

	// Another user must still be allowed
	allowedUser2, _, _ := limiter.Allow(ctx, "test", "user-2", 3, 1*time.Second)
	if !allowedUser2 {
		t.Fatalf("expected user-2 to be allowed")
	}
}

func TestLimiter_RedisBacking(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis error: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	limiter := NewLimiter(client, true)
	ctx := context.Background()

	// Limit 2 per second
	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.Allow(ctx, "auth", "ip:127.0.0.1", 2, 1*time.Second)
		if err != nil || !allowed {
			t.Fatalf("expected allowed on iteration %d, got %v", i, allowed)
		}
	}

	// 3rd request blocked
	allowed, retryAfter, err := limiter.Allow(ctx, "auth", "ip:127.0.0.1", 2, 1*time.Second)
	if err != nil || allowed {
		t.Fatalf("expected blocked on 3rd request, got allowed=%v, err=%v", allowed, err)
	}
	if retryAfter <= 0 {
		t.Fatalf("expected positive retryAfter, got %v", retryAfter)
	}

	// Fast forward past window
	mr.FastForward(2 * time.Second)

	// Now allowed again
	allowedAgain, _, err := limiter.Allow(ctx, "auth", "ip:127.0.0.1", 2, 1*time.Second)
	if err != nil || !allowedAgain {
		t.Fatalf("expected allowed after window expiry, got %v", allowedAgain)
	}
}

func TestLimiter_HTTPMiddlewareReturns429(t *testing.T) {
	limiter := NewLimiter(nil, true)
	mw := Middleware(limiter, "api", 2, 1*time.Second)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Request 1: 200
	req1 := httptest.NewRequest("GET", "/api/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	// Request 2: 200
	req2 := httptest.NewRequest("GET", "/api/test", nil)
	req2.RemoteAddr = "192.168.1.100:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	// Request 3: 429 Too Many Requests
	req3 := httptest.NewRequest("GET", "/api/test", nil)
	req3.RemoteAddr = "192.168.1.100:12345"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on 429 response")
	}

	// Different IP gets 200
	req4 := httptest.NewRequest("GET", "/api/test", nil)
	req4.RemoteAddr = "192.168.1.101:12345"
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200 for different IP, got %d", rec4.Code)
	}
}

func TestLimiter_AuthenticatedUserRateLimiting(t *testing.T) {
	limiter := NewLimiter(nil, true)
	mw := Middleware(limiter, "deploy", 1, 1*time.Second)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	userID := uuid.New()
	ctx := context.WithValue(context.Background(), middleware.UserIDKey, userID)

	req1 := httptest.NewRequest("POST", "/api/projects/1/deployments", nil).WithContext(ctx)
	req1.RemoteAddr = "10.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	// Same user from DIFFERENT IP is still rate limited by user identity!
	req2 := httptest.NewRequest("POST", "/api/projects/1/deployments", nil).WithContext(ctx)
	req2.RemoteAddr = "10.0.0.99:9999"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for authenticated user from different IP, got %d", rec2.Code)
	}
}

func TestExtractClientIP_TrustedProxies(t *testing.T) {
	trustedProxies := []string{"127.0.0.1", "10.0.0.0/8", "172.16.0.1"}

	t.Run("untrusted remote peer ignores X-Forwarded-For and X-Real-IP", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.195:4321"
		req.Header.Set("X-Forwarded-For", "198.51.100.1")
		req.Header.Set("X-Real-IP", "198.51.100.2")

		// Without trusted proxy configuration
		ip := ExtractClientIP(req)
		if ip != "203.0.113.195" {
			t.Fatalf("expected direct IP 203.0.113.195, got %s", ip)
		}

		// With trusted proxy configuration that doesn't match RemoteAddr
		ipWithTrusted := ExtractClientIP(req, trustedProxies)
		if ipWithTrusted != "203.0.113.195" {
			t.Fatalf("expected untrusted peer to be ignored, got %s", ipWithTrusted)
		}
	})

	t.Run("trusted proxy IP extracts X-Forwarded-For client IP", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", "198.51.100.42, 127.0.0.1")

		ip := ExtractClientIP(req, trustedProxies)
		if ip != "198.51.100.42" {
			t.Fatalf("expected forwarded IP 198.51.100.42 from trusted proxy, got %s", ip)
		}
	})

	t.Run("trusted CIDR proxy extracts X-Real-IP", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.1.2.3:54321"
		req.Header.Set("X-Real-IP", "198.51.100.99")

		ip := ExtractClientIP(req, trustedProxies)
		if ip != "198.51.100.99" {
			t.Fatalf("expected real IP 198.51.100.99 from CIDR trusted proxy, got %s", ip)
		}
	})
}
