package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/detector"
)

// ListOrganizations retrieves the GitHub organizations the authenticated user belongs to.
func (s *GitHubService) ListOrganizations(ctx context.Context, userID uuid.UUID) ([]GitHubOrg, error) {
	token, _ := s.getDecryptedToken(ctx, userID)
	if token == "" {
		return []GitHubOrg{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/orgs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return []GitHubOrg{}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []GitHubOrg{}, nil
	}

	var rawOrgs []struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rawOrgs); err != nil {
		return []GitHubOrg{}, nil
	}

	orgs := make([]GitHubOrg, len(rawOrgs))
	for i, o := range rawOrgs {
		orgs[i] = GitHubOrg{
			Login:     o.Login,
			AvatarURL: o.AvatarURL,
		}
	}
	return orgs, nil
}

// GetRepo verifies access and fetches metadata for a specific repository.
func (s *GitHubService) GetRepo(ctx context.Context, userID uuid.UUID, owner, repo string) (*GitHubRepo, error) {
	token, _ := s.getDecryptedTokenForRepo(ctx, userID, owner, repo)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if token != "" {
			_ = s.Disconnect(ctx, userID)
			return nil, ErrGitHubNeedsReauth
		}
		return nil, ErrGitHubNotConnected
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, ErrGitHubRateLimited
		}
		if sso := resp.Header.Get("X-GitHub-SSO"); sso != "" {
			return nil, fmt.Errorf("%w: organization SAML SSO authorization required (%s)", ErrGitHubSSORequired, sso)
		}
		if token != "" {
			return nil, ErrGitHubNeedsReauth
		}
		return nil, ErrGitHubRateLimited
	}
	if resp.StatusCode == http.StatusNotFound {
		if token == "" {
			return nil, fmt.Errorf("%w: repository not found or access denied (if this repository is private, connecting GitHub is required)", ErrGitHubRepoNotFound)
		}
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

// ListRepositories retrieves repositories accessible to the user via their GitHub authorization (App installations or OAuth).
func (s *GitHubService) ListRepositories(ctx context.Context, userID uuid.UUID, page, perPage int, orgFilter ...string) ([]GitHubRepo, bool, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 30
	}

	var targetOrg string
	if len(orgFilter) > 0 && strings.TrimSpace(orgFilter[0]) != "" {
		targetOrg = strings.TrimSpace(orgFilter[0])
	}

	// 1. Try listing from GitHub App installations if configured and linked
	if s.appManager != nil && s.appManager.IsConfigured() {
		installations, err := s.appManager.ListUserInstallations(ctx, userID)
		if err == nil && len(installations) > 0 {
			var allRepos []GitHubRepo
			hasMoreTotal := false
			for _, inst := range installations {
				if targetOrg != "" && !strings.EqualFold(inst.AccountLogin, targetOrg) {
					continue
				}
				instRepos, more, err := s.appManager.ListInstallationRepositories(ctx, inst.ID, page, perPage)
				if err == nil {
					allRepos = append(allRepos, instRepos...)
					if more {
						hasMoreTotal = true
					}
				}
			}
			if len(allRepos) > 0 {
				return allRepos, hasMoreTotal, nil
			}
		}
	}

	// 2. Fall back to user-level OAuth token / PAT
	token, scope, err := s.getDecryptedTokenAndScope(ctx, userID)
	if err != nil {
		return nil, false, err
	}

	scopes := parseOAuthScopes(scope)
	isPAT := false
	hasRepo := false
	for _, sc := range scopes {
		if sc == "pat" {
			isPAT = true
		}
		if sc == "repo" {
			hasRepo = true
		}
	}
	if !isPAT && !hasRepo && !strings.HasPrefix(token, "ghu_") && !strings.HasPrefix(token, "github_pat_") {
		return nil, false, ErrGitHubNeedsReauth
	}

	var apiURL string
	if targetOrg != "" {
		apiURL = fmt.Sprintf("https://api.github.com/orgs/%s/repos?type=all&sort=updated&direction=desc&page=%d&per_page=%d", url.PathEscape(targetOrg), page, perPage)
	} else {
		apiURL = fmt.Sprintf("https://api.github.com/user/repos?visibility=all&affiliation=owner,collaborator,organization_member&sort=updated&direction=desc&page=%d&per_page=%d", page, perPage)
	}
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
	token, _ := s.getDecryptedTokenForRepo(ctx, userID, owner, repo)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if token != "" {
			_ = s.Disconnect(ctx, userID)
			return nil, ErrGitHubNeedsReauth
		}
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
	token, tokenErr := s.getDecryptedToken(ctx, userID)
	if tokenErr != nil {
		return nil, tokenErr
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
		if token == "" && errors.Is(tokenErr, ErrGitHubNotConnected) {
			return nil, ErrGitHubNotConnected
		}
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
	token, _ := s.getDecryptedTokenForRepo(ctx, userID, owner, repo)

	if ref == "" {
		meta, err := s.GetRepo(ctx, userID, owner, repo)
		if err != nil {
			return "", fmt.Errorf("failed to determine repository default branch: %w", err)
		}
		ref = strings.TrimSpace(meta.DefaultBranch)
	}
	if ref == "" {
		return "", errors.New("repository does not define a default branch")
	}

	commitURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))

	req, err := http.NewRequestWithContext(ctx, "GET", commitURL, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ForgeLAB-App")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to resolve commit SHA: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if token != "" {
			_ = s.Disconnect(ctx, userID)
			return "", ErrGitHubNeedsReauth
		}
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
		return "", fmt.Errorf("%w: commit or branch %q not found in %s/%s", ErrGitHubRepoNotFound, ref, owner, repo)
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
