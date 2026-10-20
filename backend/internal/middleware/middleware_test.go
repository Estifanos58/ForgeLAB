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

func TestCORSMiddleware(t *testing.T) {
	allowedOrigins := []string{"http://localhost:3000", "http://127.0.0.1:3000"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mw := middleware.CORS(allowedOrigins...)(handler)

	t.Run("AllowedOrigin_PreflightOPTIONS", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/api/projects", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
			t.Fatalf("expected Access-Control-Allow-Origin to be http://localhost:3000, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatalf("expected Access-Control-Allow-Credentials: true")
		}
	})

	t.Run("DisallowedOrigin_PreflightOPTIONS_RejectedWith403", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/api/projects", nil)
		req.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for disallowed origin preflight, got %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("expected NO Access-Control-Allow-Origin header for disallowed origin, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("DisallowedOrigin_StandardGET_NoAllowOriginHeader", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/projects", nil)
		req.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("expected NO Access-Control-Allow-Origin header for disallowed origin, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}
	})
}

func TestAuthOrGuestMiddleware_OriginSecurity(t *testing.T) {
	jwtManager := auth.NewJWTManager("test-jwt-secret-key-32-chars-long!", 15*time.Minute, 7*24*time.Hour)
	allowedOrigins := []string{"http://localhost:3000", "http://127.0.0.1:3000"}

	mutationExecuted := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationExecuted = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mutated"))
	})

	mw := middleware.AuthOrGuestMiddleware(jwtManager, true, allowedOrigins...)(handler)

	t.Run("Guest_AllowedOrigin_POST_Accepted", func(t *testing.T) {
		mutationExecuted = false
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !mutationExecuted {
			t.Fatalf("expected handler to execute")
		}
	})

	t.Run("Guest_DisallowedOrigin_POST_RejectedBeforeMutation", func(t *testing.T) {
		mutationExecuted = false
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		req.Header.Set("Origin", "https://evil.com")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for untrusted origin, got %d", rec.Code)
		}
		if mutationExecuted {
			t.Fatalf("handler MUST NOT execute when origin is untrusted")
		}
	})

	t.Run("Guest_DisallowedReferer_POST_RejectedBeforeMutation", func(t *testing.T) {
		mutationExecuted = false
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		req.Header.Set("Referer", "https://evil.com/attack-page")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for untrusted referer, got %d", rec.Code)
		}
		if mutationExecuted {
			t.Fatalf("handler MUST NOT execute when referer is untrusted")
		}
	})

	t.Run("Guest_CrossSiteSecFetch_POST_RejectedBeforeMutation", func(t *testing.T) {
		mutationExecuted = false
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for Sec-Fetch-Site: cross-site, got %d", rec.Code)
		}
		if mutationExecuted {
			t.Fatalf("handler MUST NOT execute for cross-site fetch")
		}
	})

	t.Run("Guest_DirectLocalCLI_POST_Accepted", func(t *testing.T) {
		mutationExecuted = false
		// Direct curl/agent CLI without Origin or Referer
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !mutationExecuted {
			t.Fatalf("expected handler to execute for direct local CLI")
		}
	})

	t.Run("RegisteredUser_ValidJWT_POST_AlwaysAccepted", func(t *testing.T) {
		mutationExecuted = false
		userUUID := uuid.New()
		token, err := jwtManager.GenerateAccessToken(userUUID, "reg@user.com")
		if err != nil {
			t.Fatalf("failed to generate token: %v", err)
		}

		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"test"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		// Even if from another origin or no origin, authenticated registered user requests work
		req.Header.Set("Origin", "http://another-client.internal")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for registered user, got %d", rec.Code)
		}
		if !mutationExecuted {
			t.Fatalf("expected handler to execute for registered user")
		}
	})

	t.Run("Guest_RefreshTokenPresent_Returns401RefreshRequired", func(t *testing.T) {
		mutationExecuted = false
		req := httptest.NewRequest("GET", "/api/projects", nil)
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_refresh_token",
			Value: "dummy-refresh-token-value",
		})
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized when refresh token is present, got %d", rec.Code)
		}
		if mutationExecuted {
			t.Fatalf("handler MUST NOT execute when access token is missing but refresh token is present")
		}
		if !strings.Contains(rec.Body.String(), "refresh required") {
			t.Fatalf("expected response body to contain 'refresh required', got: %s", rec.Body.String())
		}
	})

	t.Run("Guest_BrowserSession_AssignsUniqueGuestIDAndSetsCookie", func(t *testing.T) {
		var capturedID uuid.UUID
		recordHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID, _ = middleware.GetUserID(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		testMw := middleware.AuthOrGuestMiddleware(jwtManager, true, allowedOrigins...)(recordHandler)

		// Simulated browser request 1 without cookie
		req1 := httptest.NewRequest("GET", "/api/projects", nil)
		req1.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")
		rec1 := httptest.NewRecorder()
		testMw.ServeHTTP(rec1, req1)

		if rec1.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec1.Code)
		}
		if capturedID == uuid.Nil || capturedID == uuid.MustParse("00000000-0000-0000-0000-000000000001") {
			t.Fatalf("expected fresh unique guest UUID for browser, got %v", capturedID)
		}
		guestCookie1 := rec1.Result().Cookies()
		var foundCookie *http.Cookie
		for _, c := range guestCookie1 {
			if c.Name == "forgelab_guest_session" {
				foundCookie = c
				break
			}
		}
		if foundCookie == nil {
			t.Fatalf("expected forgelab_guest_session cookie to be set")
		}
		if foundCookie.Value != capturedID.String() {
			t.Fatalf("cookie value %s does not match captured guest ID %s", foundCookie.Value, capturedID.String())
		}

		// Simulated browser request 2 (different browser)
		var capturedID2 uuid.UUID
		recordHandler2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID2, _ = middleware.GetUserID(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		testMw2 := middleware.AuthOrGuestMiddleware(jwtManager, true, allowedOrigins...)(recordHandler2)

		req2 := httptest.NewRequest("GET", "/api/projects", nil)
		req2.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
		rec2 := httptest.NewRecorder()
		testMw2.ServeHTTP(rec2, req2)

		if capturedID2 == capturedID {
			t.Fatalf("two different guest browser sessions must have isolated guest IDs! Both had %v", capturedID)
		}
	})

	t.Run("Guest_LoopbackCLIRequest_PreservesRegistrationFreeLocalUsage", func(t *testing.T) {
		var capturedID uuid.UUID
		recordHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID, _ = middleware.GetUserID(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		testMw := middleware.AuthOrGuestMiddleware(jwtManager, true, allowedOrigins...)(recordHandler)

		req := httptest.NewRequest("GET", "/api/projects", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("User-Agent", "curl/8.5.0")
		rec := httptest.NewRecorder()
		testMw.ServeHTTP(rec, req)

		if capturedID != uuid.MustParse("00000000-0000-0000-0000-000000000001") {
			t.Fatalf("expected local CLI curl request to default to GuestUserID, got %v", capturedID)
		}
	})
}
