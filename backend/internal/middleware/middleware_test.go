package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/middleware"
)

func TestAuthMiddleware(t *testing.T) {
	jwtManager := auth.NewJWTManager("test-jwt-secret-key-32-chars-long!", 15*time.Minute, 7*24*time.Hour)
	userID := uuid.New()
	userEmail := "test@forgelab.dev"

	validToken, err := jwtManager.GenerateAccessToken(userID, userEmail)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := middleware.GetUserID(r.Context())
		if !ok || uid != userID {
			t.Errorf("expected user ID %v, got %v", userID, uid)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mw := middleware.AuthMiddleware(jwtManager)(testHandler)

	t.Run("ValidAuthorizationHeader", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("ValidCookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_access_token",
			Value: validToken,
		})
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	// Requirement 5: Regression test proving an HTTP endpoint cannot authenticate using ?token=
	t.Run("QueryParamTokenRejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test?token="+validToken, nil)
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized for query param token on HTTP endpoint, got %d", rec.Code)
		}
	})

	t.Run("MissingAuthRejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
		}
	})
}

func TestBodyLimitMiddleware(t *testing.T) {
	mw := middleware.BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("ExceededBodySize", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader("longer than 10 bytes content"))
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d", rec.Code)
		}
	})
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	mw := middleware.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing or incorrect X-Content-Type-Options header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("missing or incorrect X-Frame-Options header")
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Errorf("missing Strict-Transport-Security for HTTPS request")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("missing or incorrect Content-Security-Policy")
	}
}
