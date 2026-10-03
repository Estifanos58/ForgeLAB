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

// rateLimitScript executes an atomic fixed-window counter in Redis.
// On the first request within a window, the counter is initialized to 1 and PEXPIRE sets
// the key TTL to window milliseconds. Subsequent increments within the TTL increase the counter.
// Once the TTL expires, the key is automatically evicted, resetting the fixed window.
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

// Limiter provides configurable fixed-window rate limiting with Redis backing and thread-safe in-memory fallback.
//
// Algorithm: Fixed-Window Counter
// Request counts are bounded within fixed duration intervals (windows). If requests exceed the
// threshold within the current window, subsequent requests are throttled with HTTP 429 and a Retry-After header.
type Limiter struct {
	client         *redis.Client
	enabled        bool
	trustedProxies []string
	mu             sync.Mutex
	memStore       map[string]*inMemoryBucket
	lastPrune      time.Time
}

// FixedWindowLimiter is an alias for Limiter to make the algorithm explicitly named.
type FixedWindowLimiter = Limiter

// NewLimiter creates a new fixed-window limiter.
// Optional trustedProxies (IP addresses or CIDRs) may be supplied to validate X-Forwarded-For and X-Real-IP headers.
func NewLimiter(client *redis.Client, enabled bool, trustedProxies ...string) *Limiter {
	return &Limiter{
		client:         client,
		enabled:        enabled,
		trustedProxies: trustedProxies,
		memStore:       make(map[string]*inMemoryBucket),
		lastPrune:      time.Now(),
	}
}

// SetTrustedProxies configures the IP addresses or CIDR blocks considered trusted reverse proxies.
func (l *Limiter) SetTrustedProxies(proxies []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trustedProxies = proxies
}

// TrustedProxies returns the configured list of trusted proxy addresses or CIDRs.
func (l *Limiter) TrustedProxies() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]string, len(l.trustedProxies))
	copy(cp, l.trustedProxies)
	return cp
}

// Allow checks if the given identifier is permitted under the rate limit for the category in the current fixed window.
// Returns: allowed (bool), retryAfter (time.Duration), err (error).
func (l *Limiter) Allow(ctx context.Context, category, identifier string, limit int, window time.Duration) (bool, time.Duration, error) {
	if !l.enabled || limit <= 0 {
		return true, 0, nil
	}

	key := fmt.Sprintf("forgelab:ratelimit:%s:%s", category, identifier)

	// Try Redis first if available (atomic fixed-window counter via Lua script)
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

	// In-memory fallback (fixed-window bucket)
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

// IsTrustedProxy checks whether remoteAddr belongs to a configured trusted proxy IP or CIDR block.
func IsTrustedProxy(remoteAddr string, trustedProxies []string) bool {
	if len(trustedProxies) == 0 {
		return false
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}

	for _, proxy := range trustedProxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}
		if strings.Contains(proxy, "/") {
			_, ipNet, err := net.ParseCIDR(proxy)
			if err == nil && ipNet.Contains(ip) {
				return true
			}
		} else {
			proxyIP := net.ParseIP(proxy)
			if proxyIP != nil && proxyIP.Equal(ip) {
				return true
			}
		}
	}

	return false
}

// ExtractClientIP extracts the client IP address from the request.
// X-Forwarded-For and X-Real-IP headers are ONLY trusted when the direct remote address
// originates from a configured trusted proxy. Otherwise, the direct RemoteAddr host IP is used.
func ExtractClientIP(r *http.Request, trustedProxies ...[]string) string {
	var proxies []string
	if len(trustedProxies) > 0 {
		proxies = trustedProxies[0]
	}

	directHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		directHost = r.RemoteAddr
	}
	directHost = strings.TrimSpace(directHost)

	// Only inspect forwarding headers if the immediate peer is a configured trusted proxy
	if IsTrustedProxy(r.RemoteAddr, proxies) {
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
			ip := strings.TrimSpace(xri)
			if ip != "" {
				return ip
			}
		}
	}

	return directHost
}

// Middleware creates an HTTP middleware that throttles requests per IP or authenticated user.
func Middleware(l *Limiter, category string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.enabled || limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Prioritize authenticated user ID, fall back to client IP (evaluating trusted proxies)
			var identifier string
			if userID, ok := middleware.GetUserID(r.Context()); ok {
				identifier = "user:" + userID.String()
			} else {
				identifier = "ip:" + ExtractClientIP(r, l.TrustedProxies())
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
