package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/detector"
)

var (
	ErrGitHubNotConnected = errors.New("github repository access is not authorized")
	ErrGitHubNeedsReauth  = errors.New("github connection requires re-authorization for repository access")
	ErrGitHubSSORequired  = errors.New("github organization saml sso authorization required")
	ErrGitHubRateLimited  = errors.New("github api rate limit exceeded")
	ErrGitHubRepoNotFound = errors.New("github repository not found or access denied")
)

// parseOAuthScopes splits comma- and/or whitespace-separated OAuth scopes reliably.
func parseOAuthScopes(scopeStr string) []string {
	fields := strings.FieldsFunc(scopeStr, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var result []string
	seen := make(map[string]bool)
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" && !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	return result
}

type GitHubRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	HTMLURL       string `json:"html_url"`
	UpdatedAt     string `json:"updated_at"`
}

type GitHubBranch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commit_sha"`
}

type GitHubStatus struct {
	Connected   bool       `json:"connected"`
	Username    string     `json:"username,omitempty"`
	Scopes      []string   `json:"scopes,omitempty"`
	NeedsReauth bool       `json:"needs_reauth"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

type GitHubService struct {
	db          *pgxpool.Pool
	encryptor   *crypto.Encryptor
	githubCfg    config.OAuthConfig
	redis        *redis.Client
	httpClient   *http.Client
	fallbackMem  sync.Map
	tokenGetter  func(ctx context.Context, userID uuid.UUID) (string, string, error)
	statusGetter func(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error)
}

func (s *GitHubService) SetHTTPClient(c *http.Client) {
	s.httpClient = c
}

func (s *GitHubService) SetTokenGetterForTest(fn func(ctx context.Context, userID uuid.UUID) (string, string, error)) {
	s.tokenGetter = fn
}

func (s *GitHubService) SetStatusGetterForTest(fn func(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error)) {
	s.statusGetter = fn
}

func NewGitHubService(
	db *pgxpool.Pool,
	encryptor *crypto.Encryptor,
	githubCfg config.OAuthConfig,
	redisClient *redis.Client,
) *GitHubService {
	return &GitHubService{
		db:         db,
		encryptor:  encryptor,
		githubCfg:  githubCfg,
		redis:      redisClient,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetStatus checks whether the user has granted repository-access OAuth permissions.
func (s *GitHubService) GetStatus(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error) {
	if s.statusGetter != nil {
		return s.statusGetter(ctx, userID)
	}
	var (
		encryptedToken []byte
		username       string
		scope          string
		updatedAt      time.Time
	)

	err := s.db.QueryRow(ctx,
		`SELECT encrypted_access_token, github_username, scope, updated_at
		 FROM github_integrations WHERE user_id = $1`,
		userID,
	).Scan(&encryptedToken, &username, &scope, &updatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &GitHubStatus{Connected: false}, nil
		}
		return nil, fmt.Errorf("failed to query github integration: %w", err)
	}

	// Verify token can be decrypted
	if _, err := s.encryptor.Decrypt(encryptedToken); err != nil {
		slog.Error("failed to decrypt github access token", "user_id", userID, "error", err)
		return &GitHubStatus{Connected: false}, nil
	}

	scopes := parseOAuthScopes(scope)
	hasRepoScope := false
	for _, sc := range scopes {
		if sc == "repo" {
			hasRepoScope = true
			break
		}
	}

	return &GitHubStatus{
		Connected:   true,
		Username:    username,
		Scopes:      scopes,
		NeedsReauth: !hasRepoScope,
		UpdatedAt:   &updatedAt,
	}, nil
}

type GitHubRepoStateRecord struct {
	UserID  string    `json:"user_id"`
	Nonce   string    `json:"nonce"`
	Expires time.Time `json:"expires"`
}

var consumeGitHubRepoStateScript = redis.NewScript(`
	local val = redis.call('GET', KEYS[1])
	if val then
		redis.call('DEL', KEYS[1])
	end
	return val
`)

// GetConnectURL initiates repository-permission authorization flow.
func (s *GitHubService) GetConnectURL(ctx context.Context, userID uuid.UUID) (authURL string, nonce string, err error) {
	if !s.githubCfg.IsConfigured() {
		return "", "", ErrProviderNotConfigured
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random nonce: %w", err)
	}
	nonce = hex.EncodeToString(nonceBytes)

	record := GitHubRepoStateRecord{
		UserID:  userID.String(),
		Nonce:   nonce,
		Expires: time.Now().Add(15 * time.Minute),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", "", err
	}

	key := "forgelab:github_repo_state:" + state

	if s.redis != nil {
		if err := s.redis.Set(ctx, key, string(payload), 15*time.Minute).Err(); err != nil {
			return "", "", fmt.Errorf("failed to store github repo state in redis: %w", err)
		}
	} else {
		s.fallbackMem.Store(key, record)
	}

	params := url.Values{}
	params.Set("client_id", s.githubCfg.ClientID)
	redirectURL := s.githubCfg.RepoRedirectURL
	if redirectURL == "" {
		redirectURL = "http://localhost:3000/api/integrations/github/callback"
	}
	params.Set("redirect_uri", redirectURL)
	params.Set("scope", "repo,read:user")
	params.Set("state", state)

	return "https://github.com/login/oauth/authorize?" + params.Encode(), nonce, nil
}

// HandleCallback completes repository-permission authorization flow.
func (s *GitHubService) HandleCallback(ctx context.Context, code, state, nonce string) (uuid.UUID, error) {
	if !s.githubCfg.IsConfigured() {
		return uuid.Nil, ErrProviderNotConfigured
	}
	if state == "" || nonce == "" {
		return uuid.Nil, ErrInvalidOAuthState
	}

	key := "forgelab:github_repo_state:" + state
	var record GitHubRepoStateRecord

	if s.redis != nil {
		res, err := consumeGitHubRepoStateScript.Run(ctx, s.redis, []string{key}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return uuid.Nil, ErrInvalidOAuthState
			}
			return uuid.Nil, fmt.Errorf("failed to query github repo state: %w", err)
		}
		rawStr, ok := res.(string)
		if !ok || rawStr == "" {
			return uuid.Nil, ErrInvalidOAuthState
		}
		if err := json.Unmarshal([]byte(rawStr), &record); err != nil {
			return uuid.Nil, ErrInvalidOAuthState
		}
	} else {
		raw, ok := s.fallbackMem.LoadAndDelete(key)
		if !ok {
			return uuid.Nil, ErrInvalidOAuthState
		}
		rec, ok := raw.(GitHubRepoStateRecord)
		if !ok || time.Now().After(rec.Expires) {
			return uuid.Nil, ErrInvalidOAuthState
		}
		record = rec
	}

	if record.Nonce != nonce {
		return uuid.Nil, ErrInvalidOAuthState
	}

	userID, err := uuid.Parse(record.UserID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user id in state: %w", err)
	}

	redirectURL := s.githubCfg.RepoRedirectURL
	if redirectURL == "" {
		redirectURL = "http://localhost:3000/api/integrations/github/callback"
	}

	// 1. Exchange code for access token
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     s.githubCfg.ClientID,
		"client_secret": s.githubCfg.ClientSecret,
		"code":          code,
		"redirect_uri":  redirectURL,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(string(tokenBody)))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("github token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, fmt.Errorf("%w: GitHub token endpoint returned HTTP %d", ErrCodeExchangeFailed, resp.StatusCode)
	}

	var tokenRes struct {
		AccessToken      string `json:"access_token"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		ErrorURI         string `json:"error_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil {
		return uuid.Nil, fmt.Errorf("%w: failed to decode GitHub token response: %v", ErrCodeExchangeFailed, err)
	}
	if tokenRes.Error != "" {
		return uuid.Nil, fmt.Errorf("%w: GitHub returned '%s' (%s). Verify that GITHUB_REPO_REDIRECT_URL in .env matches the Authorization callback URL in your GitHub OAuth App settings (expected callback: %s)",
			ErrCodeExchangeFailed, tokenRes.Error, tokenRes.ErrorDescription, redirectURL)
	}
	if tokenRes.AccessToken == "" {
		return uuid.Nil, fmt.Errorf("%w: empty access token returned by GitHub", ErrCodeExchangeFailed)
	}

	// 2. Fetch authenticated GitHub user identity
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return uuid.Nil, err
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
	userReq.Header.Set("Accept", "application/vnd.github+json")
	userReq.Header.Set("User-Agent", "ForgeLAB-App")

	userResp, err := s.httpClient.Do(userReq)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fetching github user profile failed: %w", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusOK {
		return uuid.Nil, ErrUserInfoFailed
	}

	var ghUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&ghUser); err != nil || ghUser.Login == "" {
		return uuid.Nil, ErrUserInfoFailed
	}

	// 3. Encrypt access token at rest using AES-256-GCM
	encryptedToken, err := s.encryptor.Encrypt([]byte(tokenRes.AccessToken))
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to encrypt github token: %w", err)
	}

	ghUserIDStr := fmt.Sprintf("%d", ghUser.ID)
	now := time.Now()

	// 4. Upsert into github_integrations
	_, err = s.db.Exec(ctx,
		`INSERT INTO github_integrations (user_id, encrypted_access_token, github_user_id, github_username, scope, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (user_id) DO UPDATE SET
		   encrypted_access_token = EXCLUDED.encrypted_access_token,
		   github_user_id = EXCLUDED.github_user_id,
		   github_username = EXCLUDED.github_username,
		   scope = EXCLUDED.scope,
		   updated_at = EXCLUDED.updated_at`,
		userID, encryptedToken, ghUserIDStr, ghUser.Login, tokenRes.Scope, now, now,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to save github integration: %w", err)
	}

	slog.Info("github repository integration authorized", "user_id", userID, "github_login", ghUser.Login)
	return userID, nil
}

// Disconnect revokes repository integration by removing the encrypted credential.
func (s *GitHubService) Disconnect(ctx context.Context, userID uuid.UUID) error {
	_, err := s.db.Exec(ctx, "DELETE FROM github_integrations WHERE user_id = $1", userID)
	if err != nil {
		return fmt.Errorf("failed to delete github integration: %w", err)
	}
	slog.Info("github repository integration disconnected", "user_id", userID)
	return nil
}

// getDecryptedTokenAndScope retrieves and decrypts the GitHub access token along with authorized OAuth scopes.
func (s *GitHubService) getDecryptedTokenAndScope(ctx context.Context, userID uuid.UUID) (string, string, error) {
	if s.tokenGetter != nil {
		return s.tokenGetter(ctx, userID)
	}
	if s.db == nil {
		return "", "", ErrGitHubNotConnected
	}
	var (
		encryptedToken []byte
		scope          string
	)
	err := s.db.QueryRow(ctx, "SELECT encrypted_access_token, scope FROM github_integrations WHERE user_id = $1", userID).Scan(&encryptedToken, &scope)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrGitHubNotConnected
		}
		return "", "", fmt.Errorf("failed to query github token: %w", err)
	}

	plainToken, err := s.encryptor.Decrypt(encryptedToken)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt github token: %w", err)
	}
	return string(plainToken), scope, nil
}

// getDecryptedToken retrieves and decrypts the GitHub access token for a user.
func (s *GitHubService) getDecryptedToken(ctx context.Context, userID uuid.UUID) (string, error) {
	tok, _, err := s.getDecryptedTokenAndScope(ctx, userID)
	return tok, err
}

// GetRepo verifies access and fetches metadata for a specific repository.
func (s *GitHubService) GetRepo(ctx context.Context, userID uuid.UUID, owner, repo string) (*GitHubRepo, error) {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		_ = s.Disconnect(ctx, userID)
		return nil, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return nil, fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return nil, ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rawRepo struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		Description   string `json:"description"`
		HTMLURL       string `json:"html_url"`
		UpdatedAt     string `json:"updated_at"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawRepo); err != nil {
		return nil, fmt.Errorf("failed to decode repo: %w", err)
	}

	return &GitHubRepo{
		ID:            rawRepo.ID,
		Name:          rawRepo.Name,
		FullName:      rawRepo.FullName,
		Owner:         rawRepo.Owner.Login,
		Private:       rawRepo.Private,
		DefaultBranch: rawRepo.DefaultBranch,
		Description:   rawRepo.Description,
		HTMLURL:       rawRepo.HTMLURL,
		UpdatedAt:     rawRepo.UpdatedAt,
	}, nil
}

// ListRepositories retrieves repositories accessible to the user via their GitHub authorization.
func (s *GitHubService) ListRepositories(ctx context.Context, userID uuid.UUID, page, perPage int) ([]GitHubRepo, bool, error) {
	token, _, err := s.getDecryptedTokenAndScope(ctx, userID)
	if err != nil {
		return nil, false, err
	}

	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 30
	}

	apiURL := fmt.Sprintf("https://api.github.com/user/repos?visibility=all&affiliation=owner,collaborator,organization_member&sort=updated&direction=desc&page=%d&per_page=%d", page, perPage)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list github repos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Token was revoked or expired on GitHub
		_ = s.Disconnect(ctx, userID)
		return nil, false, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, false, ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return nil, false, fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return nil, false, ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rawRepos []struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		Description   string `json:"description"`
		HTMLURL       string `json:"html_url"`
		UpdatedAt     string `json:"updated_at"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawRepos); err != nil {
		return nil, false, fmt.Errorf("failed to decode github repos: %w", err)
	}

	repos := make([]GitHubRepo, len(rawRepos))
	for i, r := range rawRepos {
		repos[i] = GitHubRepo{
			ID:            r.ID,
			Name:          r.Name,
			FullName:      r.FullName,
			Owner:         r.Owner.Login,
			Private:       r.Private,
			DefaultBranch: r.DefaultBranch,
			Description:   r.Description,
			HTMLURL:       r.HTMLURL,
			UpdatedAt:     r.UpdatedAt,
		}
	}

	hasMore := strings.Contains(resp.Header.Get("Link"), `rel="next"`) || len(rawRepos) == perPage
	return repos, hasMore, nil
}

// ListBranches retrieves branches for a specific repository.
func (s *GitHubService) ListBranches(ctx context.Context, userID uuid.UUID, owner, repo string) ([]GitHubBranch, error) {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		_ = s.Disconnect(ctx, userID)
		return nil, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return nil, fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return nil, ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rawBranches []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rawBranches); err != nil {
		return nil, fmt.Errorf("failed to decode branches: %w", err)
	}

	branches := make([]GitHubBranch, len(rawBranches))
	for i, b := range rawBranches {
		branches[i] = GitHubBranch{
			Name:      b.Name,
			CommitSHA: b.Commit.SHA,
		}
	}

	return branches, nil
}

// DetectRepo queries file indicators on GitHub and performs runtime detection.
func (s *GitHubService) DetectRepo(ctx context.Context, userID uuid.UUID, owner, repo, branch, rootDir string) (*detector.DetectionResult, error) {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	if branch == "" {
		branch = "main"
	}

	// Read top-level contents of the specified root directory
	path := strings.Trim(rootDir, "/")
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(branch))

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query repo contents: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return nil, fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubNeedsReauth, sso)
		}
		return nil, ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d: %s", resp.StatusCode, resp.Status)
	}

	var items []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	filesMap := make(map[string][]byte)
	for _, item := range items {
		if item.Type == "file" && item.DownloadURL != "" {
			nameLower := strings.ToLower(item.Name)
			if nameLower == "dockerfile" || nameLower == "package.json" ||
				nameLower == "go.mod" || nameLower == "requirements.txt" ||
				nameLower == "next.config.js" || nameLower == "next.config.mjs" ||
				nameLower == "next.config.ts" || nameLower == "vite.config.ts" ||
				nameLower == "pom.xml" || nameLower == "cargo.toml" {

				fReq, err := http.NewRequestWithContext(ctx, "GET", item.DownloadURL, nil)
				if err == nil {
					fReq.Header.Set("Authorization", "Bearer "+token)
					fReq.Header.Set("User-Agent", "ForgeLAB-App")
					fResp, err := s.httpClient.Do(fReq)
					if err == nil && fResp.StatusCode == http.StatusOK {
						b, _ := io.ReadAll(io.LimitReader(fResp.Body, 1024*1024))
						fResp.Body.Close()
						filesMap[item.Name] = b
					}
				}
			}
		}
	}

	return detector.DetectFromFiles(filesMap), nil
}

// ResolveCommitSHA resolves a branch/ref to the exact 40-character commit SHA.
func (s *GitHubService) ResolveCommitSHA(ctx context.Context, userID uuid.UUID, owner, repo, ref string) (string, error) {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return "", err
	}
	if ref == "" {
		ref = "main"
	}

	commitURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))

	req, err := http.NewRequestWithContext(ctx, "GET", commitURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to resolve commit SHA: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		_ = s.Disconnect(ctx, userID)
		return "", ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return "", ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return "", fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return "", ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned HTTP %d when resolving commit for %s", resp.StatusCode, ref)
	}

	var commitData struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commitData); err != nil {
		return "", fmt.Errorf("failed to decode commit response: %w", err)
	}
	return commitData.SHA, nil
}

// MaterializeGitHubSnapshot downloads and extracts an isolated source snapshot pinned to exact commit SHA.
func (s *GitHubService) MaterializeGitHubSnapshot(ctx context.Context, userID uuid.UUID, owner, repo, ref, destDir string) (string, error) {
	sha, err := s.ResolveCommitSHA(ctx, userID, owner, repo, ref)
	if err != nil {
		return "", fmt.Errorf("failed to resolve commit SHA for %s/%s@%s: %w", owner, repo, ref, err)
	}
	if len(sha) != 40 {
		return "", fmt.Errorf("invalid commit SHA resolved for %s/%s@%s: %s", owner, repo, ref, sha)
	}
	if err := s.AcquireRepoTarball(ctx, userID, owner, repo, sha, destDir); err != nil {
		return "", err
	}
	return sha, nil
}

// AnalyzeRepo acquires the repository archive and runs analyzer.AnalyzeRepository to produce normalized services.
// It resolves the exact commit SHA for the analyzed snapshot and cleans up temporary workspaces.
func (s *GitHubService) AnalyzeRepo(ctx context.Context, userID uuid.UUID, owner, repo, branch, rootDir string) (*analyzer.AnalysisResult, error) {
	if branch == "" {
		branch = "main"
	}

	// Resolve exact commit SHA to pin analyzed revision
	commitSHA, err := s.ResolveCommitSHA(ctx, userID, owner, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve commit for repository %s/%s@%s: %w", owner, repo, branch, err)
	}
	if len(commitSHA) != 40 {
		return nil, fmt.Errorf("invalid commit SHA for repository %s/%s@%s: %s", owner, repo, branch, commitSHA)
	}
	revision := commitSHA

	targetDir, err := os.MkdirTemp("", fmt.Sprintf("forgelab-gh-%s-%s-*", owner, repo))
	if err != nil {
		targetDir = filepath.Join(os.TempDir(), fmt.Sprintf("forgelab-gh-%s-%s-%s", owner, repo, revision))
		_ = os.RemoveAll(targetDir)
		_ = os.MkdirAll(targetDir, 0755)
	}
	defer os.RemoveAll(targetDir) // Clean up temporary GitHub analysis workspace correctly

	err = s.AcquireRepoTarball(ctx, userID, owner, repo, revision, targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire repository archive for %s/%s@%s: %w", owner, repo, revision, err)
	}

	analysisDir := targetDir
	if rootDir != "" && rootDir != "." {
		cleanRoot := filepath.Clean(filepath.FromSlash(rootDir))
		if strings.HasPrefix(cleanRoot, "..") || cleanRoot == ".." || filepath.IsAbs(cleanRoot) {
			return nil, fmt.Errorf("invalid root_dir path traversal: %s", rootDir)
		}
		analysisDir = filepath.Join(targetDir, cleanRoot)
		if fi, sErr := os.Stat(analysisDir); sErr != nil || !fi.IsDir() {
			return nil, fmt.Errorf("root_dir '%s' does not exist in repository %s/%s", rootDir, owner, repo)
		}
	}
	res, aErr := analyzer.AnalyzeRepository(analysisDir)
	if aErr != nil || res == nil || len(res.Services) == 0 {
		return nil, fmt.Errorf("failed to analyze repository in directory '%s': %v", rootDir, aErr)
	}
	res.RepositoryName = repo
	return res, nil
}

// copyDirectoryContents copies files from src into dst without leaking file descriptors.
func copyDirectoryContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || rel == ".complete" {
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}

		inFile, err := os.Open(path)
		if err != nil {
			return err
		}
		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			inFile.Close()
			return err
		}
		_, copyErr := io.Copy(outFile, inFile)
		cErr1 := inFile.Close()
		cErr2 := outFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if cErr1 != nil {
			return cErr1
		}
		return cErr2
	})
}

// AcquireRepoTarball downloads and safely extracts an authorized GitHub repository tarball.
// It verifies repository authorization before serving cached or newly downloaded snapshots.
// Private repository snapshots are strictly isolated per ForgeLAB user.
func (s *GitHubService) AcquireRepoTarball(ctx context.Context, userID uuid.UUID, owner, repo, branch, targetDir string) error {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return err
	}

	if branch == "" {
		branch = "main"
	}

	destClean := filepath.Clean(targetDir)
	_ = os.MkdirAll(destClean, 0755)

	// Security requirement: Always verify that the requesting user's credentials are authorized
	// to access this specific repository BEFORE returning any cached data or downloading anew.
	repoMeta, err := s.GetRepo(ctx, userID, owner, repo)
	if err != nil {
		return fmt.Errorf("authorization check failed for repository %s/%s: %w", owner, repo, err)
	}

	// Always resolve exact commit SHA to guarantee reproducibility and prevent SHA bypass.
	resolvedSHA, err := s.ResolveCommitSHA(ctx, userID, owner, repo, branch)
	if err != nil {
		return fmt.Errorf("failed to resolve commit SHA for %s/%s@%s: %w", owner, repo, branch, err)
	}
	if len(resolvedSHA) != 40 {
		return fmt.Errorf("invalid commit SHA resolved for %s/%s@%s: %s", owner, repo, branch, resolvedSHA)
	}

	// Authorization boundary isolation:
	// Private snapshots are isolated by user ID so another user cannot retrieve cached private code.
	var cacheDir string
	if repoMeta.Private {
		cacheDir = filepath.Join(os.TempDir(), "forgelab_gh_cache", "private", userID.String(), owner, repo, resolvedSHA)
	} else {
		cacheDir = filepath.Join(os.TempDir(), "forgelab_gh_cache", "public", owner, repo, resolvedSHA)
	}

	completeMarker := filepath.Join(cacheDir, ".complete")
	if fi, err := os.Stat(completeMarker); err == nil && !fi.IsDir() {
		if content, readErr := os.ReadFile(completeMarker); readErr == nil && strings.TrimSpace(string(content)) == resolvedSHA {
			slog.Info("reusing authorized cached github snapshot", "owner", owner, "repo", repo, "sha", resolvedSHA, "private", repoMeta.Private, "user_id", userID)
			return copyDirectoryContents(cacheDir, destClean)
		}
	}
	// Purge incomplete or corrupt cache directory
	_ = os.RemoveAll(cacheDir)

	tarURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(resolvedSHA))

	req, err := http.NewRequestWithContext(ctx, "GET", tarURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download repo tarball: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		return ErrGitHubNeedsReauth
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrGitHubRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github returned HTTP %d when downloading repository archive", resp.StatusCode)
	}

	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip stream: %w", err)
	}
	defer gzReader.Close()

	// Extract into an isolated staging directory first
	stagingDir, err := os.MkdirTemp("", fmt.Sprintf("forgelab-extract-%s-%s-*", owner, repo))
	if err != nil {
		stagingDir = filepath.Join(os.TempDir(), fmt.Sprintf("forgelab-extract-%s-%s-%s", owner, repo, resolvedSHA))
		_ = os.RemoveAll(stagingDir)
		_ = os.MkdirAll(stagingDir, 0755)
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	tarReader := tar.NewReader(gzReader)
	var rootPrefix string

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar reading error: %w", err)
		}

		// GitHub tarballs contain a top-level directory like "owner-repo-commit/"
		parts := strings.Split(strings.Trim(header.Name, "/"), "/")
		if rootPrefix == "" && len(parts) > 0 {
			rootPrefix = parts[0]
		}

		// Strip top-level directory prefix
		if len(parts) <= 1 {
			continue
		}
		relPath := strings.Join(parts[1:], string(filepath.Separator))
		targetPath := filepath.Join(stagingDir, relPath)

		// Security: prevent Zip Slip / Tar Slip path traversal
		if !strings.HasPrefix(filepath.Clean(targetPath), stagingDir+string(filepath.Separator)) {
			slog.Warn("skipping malicious file in tarball", "path", header.Name)
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}

	// Copy staging directory contents to final target directory
	if err := copyDirectoryContents(stagingDir, destClean); err != nil {
		return fmt.Errorf("failed to materialize extracted repository snapshot: %w", err)
	}

	// Populate disk cache for future reuse only when pinned to an immutable SHA and fully extracted
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0755)
		_ = copyDirectoryContents(stagingDir, cacheDir)
		_ = os.WriteFile(filepath.Join(cacheDir, ".complete"), []byte(resolvedSHA), 0644)
	}

	return nil
}
