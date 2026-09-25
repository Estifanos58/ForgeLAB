package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/forgelab/backend/internal/services"
)

// AuthHandler handles authentication-related HTTP requests.
type AuthHandler struct {
	userService *services.UserService
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userService *services.UserService) *AuthHandler {
	return &AuthHandler{userService: userService}
}

// Register handles POST /api/auth/register
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var input services.RegisterInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate input
	if input.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if input.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	if len(input.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	user, tokens, err := h.userService.Register(r.Context(), input)
	if err != nil {
		if errors.Is(err, services.ErrUserExists) {
			writeError(w, http.StatusConflict, "user with this email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register user")
		return
	}

	setAuthCookies(w, tokens)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user":   user,
		"tokens": tokens,
	})
}

// Login handles POST /api/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var input services.LoginInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if input.Email == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	user, tokens, err := h.userService.Login(r.Context(), input)
	if err != nil {
		if errors.Is(err, services.ErrInvalidPassword) {
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to login")
		return
	}

	setAuthCookies(w, tokens)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":   user,
		"tokens": tokens,
	})
}

// Refresh handles POST /api/auth/refresh
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	// Try body or cookie
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.RefreshToken == "" {
		if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
			input.RefreshToken = cookie.Value
		}
	}

	if input.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	tokens, err := h.userService.RefreshTokens(r.Context(), input.RefreshToken)
	if err != nil {
		if errors.Is(err, services.ErrTokenNotFound) ||
			errors.Is(err, services.ErrTokenRevoked) ||
			errors.Is(err, services.ErrTokenExpired) {
			clearAuthCookies(w)
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to refresh token")
		return
	}

	setAuthCookies(w, tokens)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tokens": tokens,
	})
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out successfully"})
}

// Me handles GET /api/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	user, err := h.userService.GetUserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get user")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func setAuthCookies(w http.ResponseWriter, tokens *services.AuthTokens) {
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_access_token",
		Value:    tokens.AccessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   15 * 60,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_refresh_token",
		Value:    tokens.RefreshToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_access_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_refresh_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}
