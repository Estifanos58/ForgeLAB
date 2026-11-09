package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/services"
)

func TestAuthHandler_Touch(t *testing.T) {
	jwtManager := auth.NewJWTManager("test-jwt-secret-key-at-least-32-bytes-long!", 24*time.Hour, 30*24*time.Hour)
	userSvc := services.NewUserService(nil, jwtManager)
	handler := handlers.NewAuthHandler(userSvc, nil, "http://localhost:3000", false)

	router := chi.NewRouter()
	router.Post("/api/auth/touch", handler.Touch)

	t.Run("Guest mode touches and extends guest session", func(t *testing.T) {
		handler.SetAllowGuestMode(true)
		guestID := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/touch", nil)
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_guest_session",
			Value: guestID.String(),
		})

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err := json.NewDecoder(rec.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp["status"])
		assert.Equal(t, false, resp["authenticated"])
		assert.Equal(t, true, resp["guest"])
		assert.Equal(t, guestID.String(), resp["guest_id"])

		// Verify sliding cookie is refreshed
		var foundCookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == "forgelab_guest_session" {
				foundCookie = c
				break
			}
		}
		require.NotNil(t, foundCookie)
		assert.Equal(t, guestID.String(), foundCookie.Value)
		assert.Equal(t, 86400*30, foundCookie.MaxAge)
	})

	t.Run("Valid access token cookie touches active session", func(t *testing.T) {
		handler.SetAllowGuestMode(true)
		userID := uuid.New()
		token, err := jwtManager.GenerateAccessToken(userID, "dev@example.com")
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/auth/touch", nil)
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_access_token",
			Value: token,
		})

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]interface{}
		err = json.NewDecoder(rec.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp["status"])
		assert.Equal(t, true, resp["authenticated"])
		assert.Equal(t, userID.String(), resp["user_id"])
	})

	t.Run("Valid access token with refresh token does not rotate refresh token", func(t *testing.T) {
		handler.SetAllowGuestMode(true)
		userID := uuid.New()
		token, err := jwtManager.GenerateAccessToken(userID, "dev@example.com")
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/auth/touch", nil)
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_access_token",
			Value: token,
		})
		req.AddCookie(&http.Cookie{
			Name:  "forgelab_refresh_token",
			Value: "existing-refresh-token-value",
		})

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]interface{}
		err = json.NewDecoder(rec.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp["status"])
		assert.Equal(t, true, resp["authenticated"])
		assert.Equal(t, userID.String(), resp["user_id"])

		// Ensure no new refresh token cookie was set (no rotation)
		for _, c := range rec.Result().Cookies() {
			assert.NotEqual(t, "forgelab_refresh_token", c.Name, "refresh token must NOT be rotated when access token is valid")
		}
	})

	t.Run("Guest mode disabled without tokens returns 401", func(t *testing.T) {
		handler.SetAllowGuestMode(false)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/touch", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
