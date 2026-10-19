package middleware

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/models"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// UserIDKey is the context key for the authenticated user's ID.
	UserIDKey contextKey = "user_id"
	// UserEmailKey is the context key for the authenticated user's email.
	UserEmailKey contextKey = "user_email"
	// IsGuestKey is the context key indicating whether the request is authenticated as guest.
	IsGuestKey contextKey = "is_guest"
)

// GetUserID extracts the authenticated user's ID from the request context.
func GetUserID(ctx context.Context) (uuid.UUID, bool) {
	userID, ok := ctx.Value(UserIDKey).(uuid.UUID)
	return userID, ok
}

// GetUserEmail extracts the authenticated user's email from the request context.
func GetUserEmail(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(UserEmailKey).(string)
	return email, ok
}

// IsGuestRequest extracts whether the request in context is resolved as a guest.
func IsGuestRequest(ctx context.Context) bool {
	val, ok := ctx.Value(IsGuestKey).(bool)
	return ok && val
}

// AuthMiddleware validates JWT tokens and adds user info to the request context.
func AuthMiddleware(jwtManager *auth.JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := ""

			// 1. Try Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					tokenString = parts[1]
				}
			}

			// 2. Try cookie
			if tokenString == "" {
				if cookie, err := r.Cookie("forgelab_access_token"); err == nil && cookie.Value != "" {
					tokenString = cookie.Value
				}
			}

			// Query parameter ?token= is strictly rejected for HTTP routes.
			// Only Authorization header or HttpOnly cookie are permitted.
			if tokenString == "" {
				http.Error(w, `{"error": "authentication required"}`, http.StatusUnauthorized)
				return
			}

			// Validate the token
			claims, err := jwtManager.ValidateAccessToken(tokenString)
			if err != nil {
				slog.Debug("token validation failed", "error", err)
				http.Error(w, `{"error": "invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			// Add user info to context
			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, UserEmailKey, claims.Email)
			ctx = context.WithValue(ctx, IsGuestKey, false)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IsAllowedOrigin checks whether a given origin is in the allowed origins list.
func IsAllowedOrigin(origin string, allowedOrigins []string) bool {
	if origin == "" {
		return false
	}
	normOrigin := strings.TrimRight(strings.ToLower(strings.TrimSpace(origin)), "/")
	for _, o := range allowedOrigins {
		normAllowed := strings.TrimRight(strings.ToLower(strings.TrimSpace(o)), "/")
		if normAllowed == "*" || normAllowed == normOrigin {
			return true
		}
	}
	return false
}

// AuthOrGuestMiddleware validates JWT tokens for registered users or assigns the
// shared guest identity if unauthenticated and guest mode is enabled.
// For guest state-changing requests (POST, PUT, PATCH, DELETE), incoming browser
// Origin and Referer headers are validated against allowedOrigins to prevent
// unauthorized cross-origin mutations.
func AuthOrGuestMiddleware(jwtManager *auth.JWTManager, allowGuestMode bool, allowedOrigins ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := ""

			// 1. Try Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					tokenString = parts[1]
				}
			}

			// 2. Try cookie
			if tokenString == "" {
				if cookie, err := r.Cookie("forgelab_access_token"); err == nil && cookie.Value != "" {
					tokenString = cookie.Value
				}
			}

			// If a token was provided, validate it strictly
			if tokenString != "" {
				claims, err := jwtManager.ValidateAccessToken(tokenString)
				if err != nil {
					slog.Debug("token validation failed", "error", err)
					http.Error(w, `{"error": "invalid or expired token"}`, http.StatusUnauthorized)
					return
				}

				ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
				ctx = context.WithValue(ctx, UserEmailKey, claims.Email)
				ctx = context.WithValue(ctx, IsGuestKey, false)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// If an access token is absent but a refresh token cookie is present, this client is an
			// authenticated user whose access token has expired (or was evicted by the browser).
			// We MUST NOT silently downgrade them to guest identity, as doing so causes legitimate
			// project operations to fail with "403 Forbidden: access denied" and prevents the frontend
			// from executing its automatic 401 token refresh retry.
			if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
				http.Error(w, `{"error": "access token expired or missing, refresh required"}`, http.StatusUnauthorized)
				return
			}

			// No token provided: check if guest mode is permitted on this installation
			if !allowGuestMode {
				http.Error(w, `{"error": "authentication required: guest mode disabled on this installation"}`, http.StatusUnauthorized)
				return
			}

			// Validate Origin at the request boundary for state-changing guest requests
			isStateChanging := r.Method == http.MethodPost || r.Method == http.MethodPut ||
				r.Method == http.MethodPatch || r.Method == http.MethodDelete

			if isStateChanging {
				origin := r.Header.Get("Origin")
				if origin != "" {
					if !IsAllowedOrigin(origin, allowedOrigins) {
						slog.Warn("untrusted origin rejected for guest mutation",
							"origin", origin,
							"method", r.Method,
							"path", r.URL.Path,
							"remote_addr", r.RemoteAddr,
						)
						http.Error(w, `{"error": "untrusted origin: cross-origin guest operations are forbidden"}`, http.StatusForbidden)
						return
					}
				} else {
					// Check Referer header if Origin is absent
					referer := r.Header.Get("Referer")
					if referer != "" {
						u, err := url.Parse(referer)
						if err == nil && u.Host != "" {
							refOrigin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
							if !IsAllowedOrigin(refOrigin, allowedOrigins) {
								slog.Warn("untrusted referer rejected for guest mutation",
									"referer", referer,
									"method", r.Method,
									"path", r.URL.Path,
									"remote_addr", r.RemoteAddr,
								)
								http.Error(w, `{"error": "untrusted origin: cross-origin guest operations are forbidden"}`, http.StatusForbidden)
								return
							}
						}
					}

					// Check Sec-Fetch-Site: reject cross-site browser fetches
					secFetchSite := strings.ToLower(r.Header.Get("Sec-Fetch-Site"))
					if secFetchSite == "cross-site" {
						slog.Warn("cross-site fetch rejected for guest mutation",
							"method", r.Method,
							"path", r.URL.Path,
							"remote_addr", r.RemoteAddr,
						)
						http.Error(w, `{"error": "untrusted origin: cross-origin guest operations are forbidden"}`, http.StatusForbidden)
						return
					}
				}
			}

			// Assign the single shared guest identity
			ctx := context.WithValue(r.Context(), UserIDKey, models.GuestUserID)
			ctx = context.WithValue(ctx, UserEmailKey, models.GuestUserEmail)
			ctx = context.WithValue(ctx, IsGuestKey, true)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// BodyLimit limits the maximum size of incoming request bodies.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, `{"error": "request body too large"}`, http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders applies production HTTP security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none';")

		// Apply HSTS when HTTPS is active
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}

		next.ServeHTTP(w, r)
	})
}

// RequestLogger logs HTTP request details.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"duration", duration.String(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// CORS creates a CORS middleware that permits requests from configured origins.
// Preflight OPTIONS requests and standard requests from disallowed origins are strictly
// forbidden from receiving Access-Control-Allow-Origin headers.
func CORS(allowedOriginsInputs ...string) func(http.Handler) http.Handler {
	allowedOrigins := make(map[string]bool)
	for _, input := range allowedOriginsInputs {
		for _, o := range strings.Split(input, ",") {
			trimmed := strings.TrimRight(strings.ToLower(strings.TrimSpace(o)), "/")
			if trimmed != "" {
				allowedOrigins[trimmed] = true
			}
		}
	}
	if len(allowedOrigins) == 0 {
		allowedOrigins["http://localhost:3000"] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				normOrigin := strings.TrimRight(strings.ToLower(strings.TrimSpace(origin)), "/")
				if allowedOrigins[normOrigin] || allowedOrigins["*"] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Cookie, X-Requested-With")
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Vary", "Origin")

					if r.Method == http.MethodOptions {
						w.WriteHeader(http.StatusOK)
						return
					}
				} else {
					// Origin is present but disallowed: do NOT set Access-Control-Allow-Origin!
					// Strictly reject preflight requests from disallowed origins
					if r.Method == http.MethodOptions {
						http.Error(w, `{"error": "forbidden origin"}`, http.StatusForbidden)
						return
					}
				}
			} else if r.Method == http.MethodOptions {
				// Non-browser OPTIONS request without Origin header
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ContentTypeJSON sets Content-Type to application/json for API routes.
func ContentTypeJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

// MaxBodySize limits the maximum request body size to prevent denial-of-service via huge payloads.
// Supports overriding for multipart source upload paths.
func MaxBodySize(defaultLimitBytes int64, uploadLimitBytes int64) func(http.Handler) http.Handler {
	if defaultLimitBytes <= 0 {
		defaultLimitBytes = 10 * 1024 * 1024 // 10MB
	}
	if uploadLimitBytes <= 0 {
		uploadLimitBytes = 105 * 1024 * 1024 // 105MB
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				limit := defaultLimitBytes
				if strings.HasSuffix(r.URL.Path, "/upload") || strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
					limit = uploadLimitBytes
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusResponseWriter wraps http.ResponseWriter to capture the status code.
type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker to allow WebSocket connection upgrades
func (w *statusResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher to support streaming HTTP responses
func (w *statusResponseWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter
func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
