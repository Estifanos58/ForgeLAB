package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

type IntegrationHandler struct {
	githubService *services.GitHubService
	appManager    *services.GitHubAppManager
	importService *services.RepositoryImportService
	frontendURL   string
	cookieSecure  bool
}

func NewIntegrationHandler(
	githubService *services.GitHubService,
	appManager *services.GitHubAppManager,
	importService *services.RepositoryImportService,
	frontendURL string,
	cookieSecure ...bool,
) *IntegrationHandler {
	secure := false
	if len(cookieSecure) > 0 {
		secure = cookieSecure[0]
	}
	return &IntegrationHandler{
		githubService: githubService,
		appManager:    appManager,
		importService: importService,
		frontendURL:   frontendURL,
		cookieSecure:  secure,
	}
}

func (h *IntegrationHandler) SetGitHubAppManager(app *services.GitHubAppManager) {
	h.appManager = app
}

func (h *IntegrationHandler) SetRepositoryImportService(imp *services.RepositoryImportService) {
	h.importService = imp
}

func (h *IntegrationHandler) getAuthenticatedUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	return userID, true
}

func (h *IntegrationHandler) getRegisteredUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	if models.IsGuest(userID) {
		writeError(w, http.StatusForbidden, "registration required for GitHub integration")
		return uuid.Nil, false
	}
	return userID, true
}

// GetGitHubStatus handles GET /api/integrations/github
func (h *IntegrationHandler) GetGitHubStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	status, err := h.githubService.GetStatus(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get github integration status")
		return
	}

	writeJSON(w, http.StatusOK, status)
}

// ConnectGitHub handles POST and GET /api/integrations/github/connect
func (h *IntegrationHandler) ConnectGitHub(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	authURL, nonce, err := h.githubService.GetConnectURL(r.Context(), userID)
	if err != nil {
		if errors.Is(err, services.ErrProviderNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "github oauth is not configured on the server")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to generate github authorization url")
		return
	}

	h.setOAuthNonceCookie(w, nonce)

	if r.Method == http.MethodGet {
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"url": authURL,
	})
}

// ConnectGitHubPAT handles POST /api/integrations/github/token
func (h *IntegrationHandler) ConnectGitHubPAT(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, "personal access token is required")
		return
	}

	status, err := h.githubService.ConnectWithPAT(r.Context(), userID, req.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, status)
}

// ListOrganizations handles GET /api/integrations/github/orgs
func (h *IntegrationHandler) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	orgs, err := h.githubService.ListOrganizations(r.Context(), userID)
	if err != nil {
		h.handleGitHubError(w, err, "failed to list organizations")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"organizations": orgs,
	})
}

// GitHubCallback handles GET /api/integrations/github/callback
func (h *IntegrationHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		http.Redirect(w, r, h.frontendURL+"/dashboard?error=missing_oauth_code_or_state", http.StatusFound)
		return
	}

	nonce := h.getAndClearOAuthNonceCookie(w, r)
	if nonce == "" {
		http.Redirect(w, r, h.frontendURL+"/dashboard?error=missing_or_expired_oauth_nonce", http.StatusFound)
		return
	}

	_, err := h.githubService.HandleCallback(r.Context(), code, state, nonce)
	if err != nil {
		msg := url.QueryEscape("Failed to authorize GitHub repository access: " + err.Error())
		http.Redirect(w, r, h.frontendURL+"/dashboard?error="+msg, http.StatusFound)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/dashboard?github_connected=true", http.StatusFound)
}

func (h *IntegrationHandler) setOAuthNonceCookie(w http.ResponseWriter, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_oauth_nonce_repo",
		Value:    nonce,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   900,
	})
}

func (h *IntegrationHandler) getAndClearOAuthNonceCookie(w http.ResponseWriter, r *http.Request) string {
	nonce := ""
	if cookie, err := r.Cookie("forgelab_oauth_nonce_repo"); err == nil {
		nonce = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "forgelab_oauth_nonce_repo",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return nonce
}

// DisconnectGitHub handles POST /api/integrations/github/disconnect
func (h *IntegrationHandler) DisconnectGitHub(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	if err := h.githubService.Disconnect(r.Context(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disconnect github integration")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "github repository integration disconnected successfully",
	})
}

func (h *IntegrationHandler) handleGitHubError(w http.ResponseWriter, err error, defaultMsg string) {
	if errors.Is(err, services.ErrGitHubNeedsReauth) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":        "GitHub connection requires re-authorization for repository access. Please re-authorize GitHub.",
			"needs_reauth": true,
		})
		return
	}
	if errors.Is(err, services.ErrGitHubSSORequired) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":        "Organization SAML/SSO authorization is required to access this repository. Please authorize ForgeLAB in your GitHub organization settings.",
			"sso_required": true,
		})
		return
	}
	if errors.Is(err, services.ErrGitHubNotConnected) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":         "GitHub repository access has not been granted. Please connect your GitHub account with repository permissions.",
			"not_connected": true,
		})
		return
	}
	if errors.Is(err, services.ErrGitHubRepoNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"error":     "GitHub repository not found or access denied.",
			"not_found": true,
		})
		return
	}
	if errors.Is(err, services.ErrGitHubRateLimited) {
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error":        "GitHub API rate limit exceeded. Please wait a moment or try again later.",
			"rate_limited": true,
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
		"error": fmt.Sprintf("%s: %v", defaultMsg, err),
	})
}

// GetRepository handles GET /api/integrations/github/repositories/{owner}/{repo}
func (h *IntegrationHandler) GetRepository(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "owner and repo parameters are required")
		return
	}

	repository, err := h.githubService.GetRepo(r.Context(), userID, owner, repo)
	if err != nil {
		h.handleGitHubError(w, err, "failed to get github repository")
		return
	}

	writeJSON(w, http.StatusOK, repository)
}

// ListRepositories handles GET /api/integrations/github/repositories
func (h *IntegrationHandler) ListRepositories(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	page := 1
	perPage := 30
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	if pp, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && pp > 0 && pp <= 100 {
		perPage = pp
	}

	org := r.URL.Query().Get("org")
	repos, hasMore, err := h.githubService.ListRepositories(r.Context(), userID, page, perPage, org)
	if err != nil {
		h.handleGitHubError(w, err, "failed to list github repositories")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"repositories": repos,
		"page":         page,
		"per_page":     perPage,
		"has_more":     hasMore,
	})
}

// ListBranches handles GET /api/integrations/github/repositories/{owner}/{repo}/branches
func (h *IntegrationHandler) ListBranches(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "owner and repo parameters are required")
		return
	}

	branches, err := h.githubService.ListBranches(r.Context(), userID, owner, repo)
	if err != nil {
		h.handleGitHubError(w, err, "failed to list branches")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"branches": branches,
	})
}

// GetAppInstallURL handles GET /api/integrations/github/app/install
func (h *IntegrationHandler) GetAppInstallURL(w http.ResponseWriter, r *http.Request) {
	if h.appManager == nil || !h.appManager.IsConfigured() {
		writeError(w, http.StatusServiceUnavailable, "GitHub App is not configured on the server")
		return
	}

	slug := h.appManager.AppSlug()
	if slug == "" {
		writeError(w, http.StatusServiceUnavailable, "GitHub App slug is not configured")
		return
	}

	state := uuid.New().String()
	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s", url.PathEscape(slug), state)

	writeJSON(w, http.StatusOK, map[string]string{
		"url": installURL,
	})
}

// GitHubAppCallback handles GET /api/integrations/github/app/callback
func (h *IntegrationHandler) GitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	installationIDStr := r.URL.Query().Get("installation_id")
	if installationIDStr == "" {
		http.Redirect(w, r, h.frontendURL+"/dashboard?error=missing_installation_id", http.StatusFound)
		return
	}

	installationID, err := strconv.ParseInt(installationIDStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, h.frontendURL+"/dashboard?error=invalid_installation_id", http.StatusFound)
		return
	}

	userID, ok := getUserIDFromContext(r)
	if ok && h.appManager != nil {
		details, err := h.appManager.GetInstallationDetails(r.Context(), installationID)
		if err == nil && details != nil {
			_ = h.appManager.SaveInstallation(r.Context(), userID, details)
		}
	}

	http.Redirect(w, r, h.frontendURL+"/dashboard?github_app_installed=true", http.StatusFound)
}

// StartImport handles POST /api/integrations/github/imports
func (h *IntegrationHandler) StartImport(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	if h.importService == nil {
		writeError(w, http.StatusInternalServerError, "repository import service not configured")
		return
	}

	var input services.StartImportInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	imp, err := h.importService.StartImport(r.Context(), userID, input)
	if err != nil {
		if errors.Is(err, services.ErrImportInvalidInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to start import: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, imp)
}

// GetImport handles GET /api/integrations/github/imports/{id}
func (h *IntegrationHandler) GetImport(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	importIDStr := chi.URLParam(r, "id")
	importID, err := uuid.Parse(importIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid import id")
		return
	}

	imp, err := h.importService.GetImport(r.Context(), userID, importID)
	if err != nil {
		if errors.Is(err, services.ErrImportNotFound) {
			writeError(w, http.StatusNotFound, "import not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get import status")
		return
	}

	writeJSON(w, http.StatusOK, imp)
}

// CancelImport handles POST /api/integrations/github/imports/{id}/cancel
func (h *IntegrationHandler) CancelImport(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	importIDStr := chi.URLParam(r, "id")
	importID, err := uuid.Parse(importIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid import id")
		return
	}

	imp, err := h.importService.CancelImport(r.Context(), userID, importID)
	if err != nil {
		if errors.Is(err, services.ErrImportNotFound) {
			writeError(w, http.StatusNotFound, "import not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel import")
		return
	}

	writeJSON(w, http.StatusOK, imp)
}

// RetryImport handles POST /api/integrations/github/imports/{id}/retry
func (h *IntegrationHandler) RetryImport(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getAuthenticatedUserID(w, r)
	if !ok {
		return
	}

	importIDStr := chi.URLParam(r, "id")
	importID, err := uuid.Parse(importIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid import id")
		return
	}

	imp, err := h.importService.RetryImport(r.Context(), userID, importID)
	if err != nil {
		if errors.Is(err, services.ErrImportNotFound) {
			writeError(w, http.StatusNotFound, "import not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to retry import")
		return
	}

	writeJSON(w, http.StatusOK, imp)
}

// DetectRepository handles GET /api/integrations/github/repositories/{owner}/{repo}/detect
func (h *IntegrationHandler) DetectRepository(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.getRegisteredUserID(w, r)
	if !ok {
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "owner and repo parameters are required")
		return
	}

	branch := r.URL.Query().Get("branch")
	rootDir := r.URL.Query().Get("root_dir")

	// Fallback to JSON body if sent via POST
	if r.Method == http.MethodPost && r.Body != nil {
		var req struct {
			Branch  string `json:"branch"`
			RootDir string `json:"root_dir"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Branch != "" {
			branch = req.Branch
		}
		if req.RootDir != "" {
			rootDir = req.RootDir
		}
	}

	analysis, err := h.githubService.AnalyzeRepo(r.Context(), userID, owner, repo, branch, rootDir)
	if err != nil {
		h.handleGitHubError(w, err, "failed to inspect repository for detection")
		return
	}

	primaryRuntime := "generic"
	primaryFramework := "generic"
	primaryStrategy := "auto"
	primaryPort := 8080
	primaryHealthPath := "/health"
	primaryHealthStrat := "auto"
	primaryBuildCmd := ""
	primaryStartCmd := ""

	if len(analysis.Services) > 0 {
		s0 := analysis.Services[0]
		primaryRuntime = s0.RuntimeType
		primaryFramework = s0.Framework
		primaryStrategy = s0.BuildStrategy
		primaryPort = s0.InternalPort
		primaryHealthPath = s0.HealthCheckPath
		primaryHealthStrat = s0.HealthStrategy
		primaryBuildCmd = s0.BuildCommand
		primaryStartCmd = s0.StartCommand
	}

	response := map[string]interface{}{
		"source": map[string]interface{}{
			"source_type":      "github",
			"source_reference": fmt.Sprintf("%s/%s", owner, repo),
			"branch":           branch,
			"folder_name":      repo,
			"root_dir":         rootDir,
		},
		"services":          analysis.Services,
		"total_files":       analysis.TotalFiles,
		"total_bytes":       analysis.TotalBytes,
		"runtime":           primaryRuntime,
		"framework":         primaryFramework,
		"build_strategy":    primaryStrategy,
		"suggested_port":    primaryPort,
		"health_check_path": primaryHealthPath,
		"health_strategy":   primaryHealthStrat,
		"build_command":     primaryBuildCmd,
		"start_command":     primaryStartCmd,
	}

	writeJSON(w, http.StatusOK, response)
}
