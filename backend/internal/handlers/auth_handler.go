package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

// AuthHandler handles authentication-related HTTP requests.
type AuthHandler struct {
	userService    *services.UserService
	oauthService   *services.OAuthService
	frontendURL    string
	cookieSecure   bool
	allowGuestMode bool
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(
	userService *services.UserService,
	oauthService *services.OAuthService,
	frontendURL string,
	cookieSecure bool,
) *AuthHandler {
	return &AuthHandler{
		userService:    userService,
		oauthService:   oauthService,
		frontendURL:    frontendURL,
		cookieSecure:   cookieSecure,
		allowGuestMode: true,
	}
}

// SetAllowGuestMode configures whether guest mode is enabled.
func (h *AuthHandler) SetAllowGuestMode(allow bool) {
	h.allowGuestMode = allow
}

// Mode handles GET /api/auth/mode
func (h *AuthHandler) Mode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"guest_mode_enabled": h.allowGuestMode,
		"allow_guest_mode":   h.allowGuestMode,
		"guest_user_id":      models.GuestUserID.String(),
	})
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

	h.setAuthCookies(w, tokens)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user":       user,
		"expires_in": tokens.ExpiresIn,
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

	h.setAuthCookies(w, tokens)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":       user,
		"expires_in": tokens.ExpiresIn,
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
		if errors.Is(err, services.ErrTokenRevoked) {
			// Do NOT clear cookies on revoked token: a concurrent winning refresh
			// request may have already set new valid cookies in the browser.
			// Clearing cookies here would cause the browser to lose its active session.
			writeError(w, http.StatusUnauthorized, "refresh token revoked")
			return
		}
		if errors.Is(err, services.ErrTokenNotFound) ||
			errors.Is(err, services.ErrTokenExpired) {
			h.clearAuthCookies(w)
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to refresh token")
		return
	}

	h.setAuthCookies(w, tokens)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"expires_in": tokens.ExpiresIn,
	})
}

// Touch handles POST /api/auth/touch
// It touches the session to extend session activity, avoiding unnecessary refresh-token rotation
// when the user already holds a valid access token.
// If the access token has expired or is absent but a refresh token is present, it rotates tokens.
// Proactively invoked by SessionManager on interaction or live deployment log streams.
func (h *AuthHandler) Touch(w http.ResponseWriter, r *http.Request) {
	// 1. Try validating existing access token first to avoid unnecessary token rotation
	tokenString := ""
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			tokenString = parts[1]
		}
	}
	if tokenString == "" {
		if cookie, err := r.Cookie("forgelab_access_token"); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		}
	}

	if tokenString != "" && h.userService != nil && h.userService.JWTManager() != nil {
		claims, err := h.userService.JWTManager().ValidateAccessToken(tokenString)
		if err == nil && claims != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":        "ok",
				"authenticated": true,
				"user_id":       claims.UserID,
			})
			return
		}
	}

	// 2. Access token is missing or expired: rotate using refresh token if present
	refreshToken := ""
	if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
		refreshToken = cookie.Value
	}
	if refreshToken == "" && r.Body != nil && r.ContentLength > 0 {
		var input struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		refreshToken = input.RefreshToken
	}

	if refreshToken != "" && h.userService != nil {
		tokens, err := h.userService.RefreshTokens(r.Context(), refreshToken)
		if err == nil {
			h.setAuthCookies(w, tokens)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":        "ok",
				"authenticated": true,
				"expires_in":    tokens.ExpiresIn,
			})
			return
		}
		if errors.Is(err, services.ErrTokenRevoked) {
			// Token was already safely rotated within the concurrency window
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":        "ok",
				"authenticated": true,
			})
			return
		}
	}

	// 3. Fallback to guest session if guest mode is allowed
	if h.allowGuestMode {
		guestID := middleware.ResolveGuestSession(w, r)
		if h.userService != nil {
			_ = h.userService.EnsureGuestUserWithID(r.Context(), guestID)
		}
		models.RegisterGuestID(guestID)
		if w != nil {
			http.SetCookie(w, &http.Cookie{
				Name:     "forgelab_guest_session",
				Value:    guestID.String(),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   86400 * 30, // 30 days
			})
			w.Header().Set("X-Guest-Session", guestID.String())
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":        "ok",
			"authenticated": false,
			"guest":         true,
			"guest_id":      guestID.String(),
		})
		return
	}

	writeError(w, http.StatusUnauthorized, "no active session found")
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	refreshToken := ""
	if cookie, err := r.Cookie("forgelab_refresh_token"); err == nil && cookie.Value != "" {
		refreshToken = cookie.Value
	}
	if refreshToken == "" {
		var input struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		refreshToken = input.RefreshToken
	}

	if refreshToken != "" {
		_ = h.userService.RevokeRefreshToken(r.Context(), refreshToken)
	}

	h.clearAuthCookies(w)
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
		if errors.Is(err, services.ErrUserNotFound) {
			h.clearAuthCookies(w)
			writeError(w, http.StatusUnauthorized, "authenticated user account no longer exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get user")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// GoogleLogin handles GET /api/auth/google
func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	authURL, nonce, err := h.oauthService.GetGoogleAuthURL(r.Context())
	if err != nil {
		if errors.Is(err, services.ErrProviderNotConfigured) {
			writeError(w, http.StatusBadRequest, "Google authentication is not configured. Please set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET.")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to initiate Google authentication")
		return
	}
	h.setOAuthNonceCookie(w, nonce)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// GoogleCallback handles GET /api/auth/google/callback
func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	errorParam := r.URL.Query().Get("error")
	if errorParam != "" {
		h.redirectWithError(w, r, "Google sign-in was canceled or encountered an error")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		h.redirectWithError(w, r, "Missing authorization code or state from Google")
		return
	}

	nonce := h.getAndClearOAuthNonceCookie(w, r)
	if nonce == "" {
		h.redirectWithError(w, r, "OAuth session expired or CSRF nonce missing. Please try again.")
		return
	}

	userInfo, err := h.oauthService.HandleGoogleCallback(r.Context(), code, state, nonce)
	if err != nil {
		slog.Error("google oauth callback failed", "error", err)
		if errors.Is(err, services.ErrInvalidOAuthState) {
			h.redirectWithError(w, r, "OAuth session expired or CSRF state invalid. Please try again.")
			return
		}
		if errors.Is(err, services.ErrProviderNotConfigured) {
			h.redirectWithError(w, r, "Google authentication is not configured on the server")
			return
		}
		h.redirectWithError(w, r, "Failed to authenticate with Google")
		return
	}

	_, tokens, err := h.userService.FindOrCreateOAuthUser(
		r.Context(),
		userInfo.Provider,
		userInfo.Subject,
		userInfo.Email,
		userInfo.DisplayName,
		userInfo.EmailVerified,
	)
	if err != nil {
		slog.Error("failed to find or create oauth user", "error", err)
		h.redirectWithError(w, r, err.Error())
		return
	}

	h.setAuthCookies(w, tokens)
	http.Redirect(w, r, h.frontendURL+"/dashboard", http.StatusTemporaryRedirect)
}

// GitHubLogin handles GET /api/auth/github
func (h *AuthHandler) GitHubLogin(w http.ResponseWriter, r *http.Request) {
	authURL, nonce, err := h.oauthService.GetGitHubAuthURL(r.Context())
	if err != nil {
		if errors.Is(err, services.ErrProviderNotConfigured) {
			writeError(w, http.StatusBadRequest, "GitHub authentication is not configured. Please set GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET.")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to initiate GitHub authentication")
		return
	}
	h.setOAuthNonceCookie(w, nonce)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// GitHubCallback handles GET /api/auth/github/callback
func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	errorParam := r.URL.Query().Get("error")
	if errorParam != "" {
		h.redirectWithError(w, r, "GitHub sign-in was canceled or encountered an error")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		h.redirectWithError(w, r, "Missing authorization code or state from GitHub")
		return
	}

	nonce := h.getAndClearOAuthNonceCookie(w, r)
	if nonce == "" {
		h.redirectWithError(w, r, "OAuth session expired or CSRF nonce missing. Please try again.")
		return
	}

	userInfo, err := h.oauthService.HandleGitHubCallback(r.Context(), code, state, nonce)
	if err != nil {
		slog.Error("github oauth callback failed", "error", err)
		if errors.Is(err, services.ErrInvalidOAuthState) {
			h.redirectWithError(w, r, "OAuth session expired or CSRF state invalid. Please try again.")
			return
		}
		if errors.Is(err, services.ErrProviderNotConfigured) {
			h.redirectWithError(w, r, "GitHub authentication is not configured on the server")
			return
		}
		h.redirectWithError(w, r, "Failed to authenticate with GitHub")
		return
	}

	_, tokens, err := h.userService.FindOrCreateOAuthUser(
		r.Context(),
		userInfo.Provider,
		userInfo.Subject,
		userInfo.Email,
		userInfo.DisplayName,
		userInfo.EmailVerified,
	)
	if err != nil {
		slog.Error("failed to find or create oauth user", "error", err)
		h.redirectWithError(w, r, err.Error())
		return
	}

	h.setAuthCookies(w, tokens)
	http.Redirect(w, r, h.frontendURL+"/dashboard", http.StatusTemporaryRedirect)
}

func (h *AuthHandler) setOAuthNonceCookie(w http.ResponseWriter, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_oauth_nonce",
		Value:    nonce,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
}

func (h *AuthHandler) getAndClearOAuthNonceCookie(w http.ResponseWriter, r *http.Request) string {
	nonce := ""
	if cookie, err := r.Cookie("forgelab_oauth_nonce"); err == nil {
		nonce = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_oauth_nonce",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return nonce
}

func (h *AuthHandler) redirectWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	loginURL := fmt.Sprintf("%s/login?error=%s", h.frontendURL, url.QueryEscape(errMsg))
	http.Redirect(w, r, loginURL, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) setAuthCookies(w http.ResponseWriter, tokens *services.AuthTokens) {
	accessMaxAge := 24 * 3600
	if h.userService != nil {
		if dur := int(h.userService.AccessTokenExpiry().Seconds()); dur > 0 {
			accessMaxAge = dur
		}
	}
	if tokens != nil && tokens.ExpiresIn > 0 {
		accessMaxAge = tokens.ExpiresIn
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_access_token",
		Value:    tokens.AccessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   accessMaxAge,
	})
	refreshMaxAge := 30 * 24 * 3600
	if h.userService != nil {
		if dur := int(h.userService.RefreshTokenExpiry().Seconds()); dur > 0 {
			refreshMaxAge = dur
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_refresh_token",
		Value:    tokens.RefreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   refreshMaxAge,
	})
}

func (h *AuthHandler) clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_access_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_refresh_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
