package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/forgelab/backend/internal/services"
)

type IntegrationHandler struct {
	githubService *services.GitHubService
	frontendURL   string
}

func NewIntegrationHandler(githubService *services.GitHubService, frontendURL string) *IntegrationHandler {
	return &IntegrationHandler{
		githubService: githubService,
		frontendURL:   frontendURL,
	}
}

// GetGitHubStatus handles GET /api/integrations/github
func (h *IntegrationHandler) GetGitHubStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	authURL, err := h.githubService.GetConnectURL(r.Context(), userID)
	if err != nil {
		if errors.Is(err, services.ErrProviderNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "github oauth is not configured on the server")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to generate github authorization url")
		return
	}

	if r.Method == http.MethodGet {
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"url": authURL,
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

	_, err := h.githubService.HandleCallback(r.Context(), code, state)
	if err != nil {
		msg := url.QueryEscape("Failed to authorize GitHub repository access: " + err.Error())
		http.Redirect(w, r, h.frontendURL+"/dashboard?error="+msg, http.StatusFound)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/dashboard?github_connected=true", http.StatusFound)
}

// DisconnectGitHub handles POST /api/integrations/github/disconnect
func (h *IntegrationHandler) DisconnectGitHub(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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

// ListRepositories handles GET /api/integrations/github/repositories
func (h *IntegrationHandler) ListRepositories(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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

	repos, err := h.githubService.ListRepositories(r.Context(), userID, page, perPage)
	if err != nil {
		if errors.Is(err, services.ErrGitHubNotConnected) {
			writeError(w, http.StatusForbidden, "github repository access has not been granted. Please connect GitHub repository permissions.")
			return
		}
		if errors.Is(err, services.ErrGitHubRateLimited) {
			writeError(w, http.StatusTooManyRequests, "github api rate limit exceeded")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list github repositories")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"repositories": repos,
		"page":         page,
		"per_page":     perPage,
	})
}

// ListBranches handles GET /api/integrations/github/repositories/{owner}/{repo}/branches
func (h *IntegrationHandler) ListBranches(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
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
		if errors.Is(err, services.ErrGitHubNotConnected) {
			writeError(w, http.StatusForbidden, "github repository access has not been granted")
			return
		}
		if errors.Is(err, services.ErrGitHubRepoNotFound) {
			writeError(w, http.StatusNotFound, "github repository not found or access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list branches")
		return
	}

	writeJSON(w, http.StatusOK, branches)
}

// DetectRepository handles POST /api/integrations/github/repositories/{owner}/{repo}/detect
func (h *IntegrationHandler) DetectRepository(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "owner and repo parameters are required")
		return
	}

	var req struct {
		Branch  string `json:"branch"`
		RootDir string `json:"root_dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Branch == "" {
		req.Branch = r.URL.Query().Get("branch")
	}
	if req.RootDir == "" {
		req.RootDir = r.URL.Query().Get("root_dir")
	}

	detection, err := h.githubService.DetectRepo(r.Context(), userID, owner, repo, req.Branch, req.RootDir)
	if err != nil {
		if errors.Is(err, services.ErrGitHubNotConnected) {
			writeError(w, http.StatusForbidden, "github repository access has not been granted")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to inspect repository for detection: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detection)
}
