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

	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/detector"
)

var (
	ErrGitHubNotConnected = errors.New("github repository access is not authorized")
	ErrGitHubRateLimited  = errors.New("github api rate limit exceeded")
	ErrGitHubRepoNotFound = errors.New("github repository not found or access denied")
	ErrGitHubOAuthFailed  = errors.New("github authorization failed")
)

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
	Connected bool       `json:"connected"`
	Username  string     `json:"username,omitempty"`
	Scopes    []string   `json:"scopes,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type GitHubService struct {
	db          *pgxpool.Pool
	encryptor   *crypto.Encryptor
	githubCfg   config.OAuthConfig
	redis       *redis.Client
	httpClient  *http.Client
	fallbackMem sync.Map
}

func NewGitHubService(
	db *pgxpool.Pool,
	encryptor *crypto.Encryptor,
	githubCfg config.OAuthConfig,
	redisClient *redis.Client,
) *GitHubService {
	return &GitHubService{
		db:          db,
		encryptor:   encryptor,
		githubCfg:   githubCfg,
		redis:       redisClient,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// GetStatus checks whether the user has granted repository-access OAuth permissions.
func (s *GitHubService) GetStatus(ctx context.Context, userID uuid.UUID) (*GitHubStatus, error) {
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

	scopes := strings.Split(scope, ",")
	for i := range scopes {
		scopes[i] = strings.TrimSpace(scopes[i])
	}

	return &GitHubStatus{
		Connected: true,
		Username:  username,
		Scopes:    scopes,
		UpdatedAt: &updatedAt,
	}, nil
}

// GetConnectURL initiates repository-permission authorization flow.
func (s *GitHubService) GetConnectURL(ctx context.Context, userID uuid.UUID) (string, error) {
	if !s.githubCfg.IsConfigured() {
		return "", ErrProviderNotConfigured
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random state: %w", err)
	}
	state := hex.EncodeToString(b)
	key := "forgelab:github_repo_state:" + state

	if s.redis != nil {
		if err := s.redis.Set(ctx, key, userID.String(), 15*time.Minute).Err(); err != nil {
			return "", fmt.Errorf("failed to store github repo state in redis: %w", err)
		}
	} else {
		s.fallbackMem.Store(key, memoryState{
			provider: userID.String(),
			expires:  time.Now().Add(15 * time.Minute),
		})
	}

	params := url.Values{}
	params.Set("client_id", s.githubCfg.ClientID)
	redirectURL := s.githubCfg.RepoRedirectURL
	if redirectURL == "" {
		redirectURL = s.githubCfg.RedirectURL
	}
	params.Set("redirect_uri", redirectURL)
	params.Set("scope", "repo,read:user")
	params.Set("state", state)

	return "https://github.com/login/oauth/authorize?" + params.Encode(), nil
}

// HandleCallback completes repository-permission authorization flow.
func (s *GitHubService) HandleCallback(ctx context.Context, code, state string) (uuid.UUID, error) {
	if !s.githubCfg.IsConfigured() {
		return uuid.Nil, ErrProviderNotConfigured
	}

	key := "forgelab:github_repo_state:" + state
	var userIDStr string

	if s.redis != nil {
		val, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			return uuid.Nil, ErrInvalidOAuthState
		}
		_ = s.redis.Del(ctx, key)
		userIDStr = val
	} else {
		raw, ok := s.fallbackMem.LoadAndDelete(key)
		if !ok {
			return uuid.Nil, ErrInvalidOAuthState
		}
		ms, ok := raw.(memoryState)
		if !ok || time.Now().After(ms.expires) {
			return uuid.Nil, ErrInvalidOAuthState
		}
		userIDStr = ms.provider
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user id in state: %w", err)
	}

	// 1. Exchange code for access token
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     s.githubCfg.ClientID,
		"client_secret": s.githubCfg.ClientSecret,
		"code":          code,
		"redirect_uri":  s.githubCfg.RepoRedirectURL,
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
		return uuid.Nil, ErrCodeExchangeFailed
	}

	var tokenRes struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil || tokenRes.AccessToken == "" {
		return uuid.Nil, ErrCodeExchangeFailed
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

// getDecryptedToken retrieves and decrypts the GitHub access token for a user.
func (s *GitHubService) getDecryptedToken(ctx context.Context, userID uuid.UUID) (string, error) {
	if s.db == nil {
		return "", ErrGitHubNotConnected
	}
	var encryptedToken []byte
	err := s.db.QueryRow(ctx, "SELECT encrypted_access_token FROM github_integrations WHERE user_id = $1", userID).Scan(&encryptedToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrGitHubNotConnected
		}
		return "", fmt.Errorf("failed to query github token: %w", err)
	}

	plainToken, err := s.encryptor.Decrypt(encryptedToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt github token: %w", err)
	}
	return string(plainToken), nil
}

// ListRepositories retrieves repositories accessible to the user via their GitHub authorization.
func (s *GitHubService) ListRepositories(ctx context.Context, userID uuid.UUID, page, perPage int) ([]GitHubRepo, error) {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 30
	}

	apiURL := fmt.Sprintf("https://api.github.com/user/repos?sort=updated&direction=desc&page=%d&per_page=%d", page, perPage)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list github repos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Token was revoked or expired on GitHub
		_ = s.Disconnect(ctx, userID)
		return nil, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, ErrGitHubRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d", resp.StatusCode)
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
		return nil, fmt.Errorf("failed to decode github repos: %w", err)
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

	return repos, nil
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

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrGitHubRepoNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = s.Disconnect(ctx, userID)
		return nil, ErrGitHubNotConnected
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

	if resp.StatusCode != http.StatusOK {
		// Fallback to generic detection if contents cannot be listed
		return &detector.DetectionResult{
			Runtime:         "generic",
			Framework:       "Generic Application",
			BuildStrategy:   "auto",
			SuggestedPort:   8080,
			HealthCheckPath: "/health",
			HealthStrategy:  "auto",
		}, nil
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

// AcquireRepoTarball downloads and safely extracts an authorized GitHub repository tarball.
func (s *GitHubService) AcquireRepoTarball(ctx context.Context, userID uuid.UUID, owner, repo, branch, targetDir string) error {
	token, err := s.getDecryptedToken(ctx, userID)
	if err != nil {
		return err
	}

	if branch == "" {
		branch = "main"
	}

	tarURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))

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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github returned HTTP %d when downloading repository archive", resp.StatusCode)
	}

	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip stream: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	destClean := filepath.Clean(targetDir)
	_ = os.MkdirAll(destClean, 0755)

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
		targetPath := filepath.Join(destClean, relPath)

		// Security: prevent Zip Slip / Tar Slip path traversal
		if !strings.HasPrefix(filepath.Clean(targetPath), destClean+string(filepath.Separator)) {
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

	return nil
}
