package ratelimit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/forgelab/backend/internal/middleware"
	"github.com/redis/go-redis/v9"
)

var rateLimitScript = redis.NewScript(`
	local current = redis.call("INCR", KEYS[1])
	if current == 1 then
		redis.call("PEXPIRE", KEYS[1], ARGV[1])
	end
	local ttl = redis.call("PTTL", KEYS[1])
	return {current, ttl}
`)

type inMemoryBucket struct {
	count     int
	expiresAt time.Time
}

// Limiter provides configurable rate limiting with Redis backing and thread-safe in-memory fallback.
type Limiter struct {
	client    *redis.Client
	enabled   bool
	mu        sync.Mutex
	memStore  map[string]*inMemoryBucket
	lastPrune time.Time
}

func NewLimiter(client *redis.Client, enabled bool) *Limiter {
	return &Limiter{
		client:    client,
		enabled:   enabled,
		memStore:  make(map[string]*inMemoryBucket),
		lastPrune: time.Now(),
	}
}

// Allow checks if the given identifier is permitted under the rate limit for the category.
// Returns: allowed (bool), retryAfter (time.Duration), err (error).
func (l *Limiter) Allow(ctx context.Context, category, identifier string, limit int, window time.Duration) (bool, time.Duration, error) {
	if !l.enabled || limit <= 0 {
		return true, 0, nil
	}

	key := fmt.Sprintf("forgelab:ratelimit:%s:%s", category, identifier)

	// Try Redis first if available
	if l.client != nil {
		res, err := rateLimitScript.Run(ctx, l.client, []string{key}, window.Milliseconds()).Result()
		if err == nil {
			if arr, ok := res.([]interface{}); ok && len(arr) == 2 {
				current := arr[0].(int64)
				ttlMs := arr[1].(int64)
				retryAfter := time.Duration(ttlMs) * time.Millisecond
				if retryAfter < 0 {
					retryAfter = window
				}
				if int(current) > limit {
					return false, retryAfter, nil
				}
				return true, 0, nil
			}
		}
		// If Redis encounters a transient error, fall through to in-memory limiter to avoid blocking traffic
	}

	// In-memory fallback
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	// Periodic prune every minute
	if now.Sub(l.lastPrune) > 1*time.Minute {
		for k, b := range l.memStore {
			if now.After(b.expiresAt) {
				delete(l.memStore, k)
			}
		}
		l.lastPrune = now
	}

	b, exists := l.memStore[key]
	if !exists || now.After(b.expiresAt) {
		l.memStore[key] = &inMemoryBucket{
			count:     1,
			expiresAt: now.Add(window),
		}
		return true, 0, nil
	}

	b.count++
	if b.count > limit {
		return false, b.expiresAt.Sub(now), nil
	}

	return true, 0, nil
}

// ExtractClientIP extracts the client IP address from the request RemoteAddr, X-Forwarded-For, or X-Real-IP.
func ExtractClientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}

	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return strings.TrimSpace(xri)
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// Middleware creates an HTTP middleware that throttles requests per IP or authenticated user.
func Middleware(l *Limiter, category string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.enabled || limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Prioritize authenticated user ID, fall back to client IP
			var identifier string
			if userID, ok := middleware.GetUserID(r.Context()); ok {
				identifier = "user:" + userID.String()
			} else {
				identifier = "ip:" + ExtractClientIP(r)
			}

			allowed, retryAfter, err := l.Allow(r.Context(), category, identifier, limit, window)
			if err != nil {
				// Fail open on rate limiter internal error to prevent availability loss
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				retryAfterSec := int(retryAfter.Seconds())
				if retryAfterSec <= 0 {
					retryAfterSec = 1
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error": "Too Many Requests", "message": "rate limit exceeded, please retry later"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
