package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/config"
)

var (
	ErrProviderNotConfigured = errors.New("oauth provider is not configured")
	ErrInvalidOAuthState     = errors.New("invalid or expired oauth state parameter")
	ErrCodeExchangeFailed    = errors.New("failed to exchange oauth authorization code")
	ErrUserInfoFailed        = errors.New("failed to retrieve user profile from oauth provider")
)

// OAuthUserInfo represents the normalized user identity returned by an OAuth provider.
type OAuthUserInfo struct {
	Provider      string
	Subject       string
	Email         string
	DisplayName   string
	EmailVerified bool
}

// OAuthStateRecord stores metadata bound to the OAuth state parameter.
type OAuthStateRecord struct {
	Provider string    `json:"provider"`
	Nonce    string    `json:"nonce"`
	Expires  time.Time `json:"expires"`
}

var consumeOAuthStateScript = redis.NewScript(`
	local val = redis.call('GET', KEYS[1])
	if val then
		redis.call('DEL', KEYS[1])
	end
	return val
`)

// OAuthService handles Google and GitHub OAuth 2.0 flows.
type OAuthService struct {
	googleCfg   config.OAuthConfig
	githubCfg   config.OAuthConfig
	redis       *redis.Client
	httpClient  *http.Client
	fallbackMem sync.Map
}

// NewOAuthService creates a new OAuthService.
func NewOAuthService(googleCfg, githubCfg config.OAuthConfig, redisClient *redis.Client) *OAuthService {
	return &OAuthService{
		googleCfg: googleCfg,
		githubCfg: githubCfg,
		redis:     redisClient,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GenerateState creates a cryptographically secure, single-use state token and session nonce, storing it in Redis.
func (s *OAuthService) GenerateState(ctx context.Context, provider string) (string, string, error) {
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	record := OAuthStateRecord{
		Provider: provider,
		Nonce:    nonce,
		Expires:  time.Now().Add(10 * time.Minute),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", "", err
	}

	key := "forgelab:oauth:state:" + state

	if s.redis != nil {
		if err := s.redis.Set(ctx, key, string(payload), 10*time.Minute).Err(); err != nil {
			return "", "", fmt.Errorf("failed to store oauth state in redis: %w", err)
		}
	} else {
		s.fallbackMem.Store(key, record)
	}

	return state, nonce, nil
}

// ValidateState validates and single-use consumes the state parameter and matches the session nonce.
func (s *OAuthService) ValidateState(ctx context.Context, provider, state, nonce string) error {
	if state == "" || nonce == "" {
		return ErrInvalidOAuthState
	}
	key := "forgelab:oauth:state:" + state

	var record OAuthStateRecord

	if s.redis != nil {
		res, err := consumeOAuthStateScript.Run(ctx, s.redis, []string{key}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return ErrInvalidOAuthState
			}
			return fmt.Errorf("failed to atomically consume oauth state: %w", err)
		}
		rawStr, ok := res.(string)
		if !ok || rawStr == "" {
			return ErrInvalidOAuthState
		}
		if err := json.Unmarshal([]byte(rawStr), &record); err != nil {
			return ErrInvalidOAuthState
		}
	} else {
		raw, ok := s.fallbackMem.LoadAndDelete(key)
		if !ok {
			return ErrInvalidOAuthState
		}
		rec, ok := raw.(OAuthStateRecord)
		if !ok || time.Now().After(rec.Expires) {
			return ErrInvalidOAuthState
		}
		record = rec
	}

	if record.Provider != provider || record.Nonce != nonce {
		return ErrInvalidOAuthState
	}

	return nil
}

// GetGoogleAuthURL returns the authorization URL and browser nonce for Google OAuth.
func (s *OAuthService) GetGoogleAuthURL(ctx context.Context) (authURL string, nonce string, err error) {
	if !s.googleCfg.IsConfigured() {
		return "", "", ErrProviderNotConfigured
	}

	state, nonce, err := s.GenerateState(ctx, "google")
	if err != nil {
		return "", "", err
	}

	params := url.Values{}
	params.Set("client_id", s.googleCfg.ClientID)
	params.Set("redirect_uri", s.googleCfg.RedirectURL)
	params.Set("response_type", "code")
	params.Set("scope", "openid email profile")
	params.Set("state", state)
	params.Set("access_type", "online")
	params.Set("prompt", "select_account")

	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode(), nonce, nil
}

// HandleGoogleCallback exchanges authorization code and fetches profile info.
func (s *OAuthService) HandleGoogleCallback(ctx context.Context, code, state, nonce string) (*OAuthUserInfo, error) {
	if !s.googleCfg.IsConfigured() {
		return nil, ErrProviderNotConfigured
	}

	if err := s.ValidateState(ctx, "google", state, nonce); err != nil {
		return nil, err
	}

	// 1. Exchange code for access token
	tokenData := url.Values{}
	tokenData.Set("code", code)
	tokenData.Set("client_id", s.googleCfg.ClientID)
	tokenData.Set("client_secret", s.googleCfg.ClientSecret)
	tokenData.Set("redirect_uri", s.googleCfg.RedirectURL)
	tokenData.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(tokenData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrCodeExchangeFailed
	}

	var tokenRes struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil || tokenRes.AccessToken == "" {
		return nil, ErrCodeExchangeFailed
	}

	// 2. Fetch user profile from OpenID userinfo endpoint
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return nil, err
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)

	userResp, err := s.httpClient.Do(userReq)
	if err != nil {
		return nil, fmt.Errorf("fetching google user info failed: %w", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusOK {
		return nil, ErrUserInfoFailed
	}

	var googleUser struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&googleUser); err != nil {
		return nil, ErrUserInfoFailed
	}

	if googleUser.Sub == "" {
		return nil, fmt.Errorf("google userinfo missing subject (sub)")
	}

	return &OAuthUserInfo{
		Provider:      "google",
		Subject:       googleUser.Sub,
		Email:         strings.ToLower(strings.TrimSpace(googleUser.Email)),
		DisplayName:   googleUser.Name,
		EmailVerified: googleUser.EmailVerified,
	}, nil
}

// GetGitHubAuthURL returns the authorization URL and browser nonce for GitHub OAuth.
func (s *OAuthService) GetGitHubAuthURL(ctx context.Context) (authURL string, nonce string, err error) {
	if !s.githubCfg.IsConfigured() {
		return "", "", ErrProviderNotConfigured
	}

	state, nonce, err := s.GenerateState(ctx, "github")
	if err != nil {
		return "", "", err
	}

	params := url.Values{}
	params.Set("client_id", s.githubCfg.ClientID)
	params.Set("redirect_uri", s.githubCfg.RedirectURL)
	params.Set("scope", "read:user user:email")
	params.Set("state", state)

	return "https://github.com/login/oauth/authorize?" + params.Encode(), nonce, nil
}

// HandleGitHubCallback exchanges authorization code and fetches profile info.
func (s *OAuthService) HandleGitHubCallback(ctx context.Context, code, state, nonce string) (*OAuthUserInfo, error) {
	if !s.githubCfg.IsConfigured() {
		return nil, ErrProviderNotConfigured
	}

	if err := s.ValidateState(ctx, "github", state, nonce); err != nil {
		return nil, err
	}

	// 1. Exchange code for access token
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     s.githubCfg.ClientID,
		"client_secret": s.githubCfg.ClientSecret,
		"code":          code,
		"redirect_uri":  s.githubCfg.RedirectURL,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(string(tokenBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrCodeExchangeFailed
	}

	var tokenRes struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil || tokenRes.AccessToken == "" {
		return nil, ErrCodeExchangeFailed
	}

	// 2. Fetch authenticated GitHub user
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
	userReq.Header.Set("Accept", "application/vnd.github+json")
	userReq.Header.Set("User-Agent", "ForgeLAB-Auth")

	userResp, err := s.httpClient.Do(userReq)
	if err != nil {
		return nil, fmt.Errorf("fetching github user failed: %w", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusOK {
		return nil, ErrUserInfoFailed
	}

	var ghUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&ghUser); err != nil {
		return nil, ErrUserInfoFailed
	}

	if ghUser.ID == 0 {
		return nil, fmt.Errorf("github response missing user ID")
	}

	subject := strconv.FormatInt(ghUser.ID, 10)
	displayName := ghUser.Name
	if displayName == "" {
		displayName = ghUser.Login
	}

	email := strings.ToLower(strings.TrimSpace(ghUser.Email))
	emailVerified := false

	// If email not returned or to verify, query /user/emails
	emailReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
	if err == nil {
		emailReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
		emailReq.Header.Set("Accept", "application/vnd.github+json")
		emailReq.Header.Set("User-Agent", "ForgeLAB-Auth")

		emailResp, err := s.httpClient.Do(emailReq)
		if err == nil && emailResp.StatusCode == http.StatusOK {
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			bodyBytes, _ := io.ReadAll(emailResp.Body)
			emailResp.Body.Close()
			if json.Unmarshal(bodyBytes, &emails) == nil {
				// Search for primary verified
				for _, e := range emails {
					if e.Primary && e.Verified {
						email = strings.ToLower(strings.TrimSpace(e.Email))
						emailVerified = true
						break
					}
				}
				// If not found, any verified
				if !emailVerified {
					for _, e := range emails {
						if e.Verified {
							email = strings.ToLower(strings.TrimSpace(e.Email))
							emailVerified = true
							break
						}
					}
				}
			}
		}
	}

	if email == "" {
		// Fallback email based on GitHub username if no verified email is obtainable
		email = fmt.Sprintf("%s@users.noreply.github.com", ghUser.Login)
		emailVerified = true
	}

	return &OAuthUserInfo{
		Provider:      "github",
		Subject:       subject,
		Email:         email,
		DisplayName:   displayName,
		EmailVerified: emailVerified,
	}, nil
}
