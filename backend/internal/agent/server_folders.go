package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/discovery"
)

// handleSelectFolder opens the native OS directory chooser dialog
func (s *AgentServer) handleSelectFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	selectedPath, err := s.picker.PickFolder(r.Context(), req.Title)
	if err != nil {
		if errors.Is(err, ErrPickerCancelled) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"cancelled": true,
			})
			return
		}
		if errors.Is(err, ErrPickerBusy) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "folder picker is already in progress",
			})
			return
		}
		http.Error(w, fmt.Sprintf("Failed to open native folder picker: %v", err), http.StatusInternalServerError)
		return
	}

	token, _ := r.Context().Value("agent_token").(string)
	session, err := s.registerDirectory(selectedPath, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to inspect selected directory: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

// handleSelectPath directly validates and analyzes a path (e.g. for automated CLI/tests)
func (s *AgentServer) handleSelectPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	token, _ := r.Context().Value("agent_token").(string)
	session, err := s.registerDirectory(req.Path, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Path validation failed: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

func (s *AgentServer) notifyBackendSession(token string, sourceID uuid.UUID, folderName string, sourcePath string) {
	if s.backendURL == "" {
		return
	}
	validateURL := fmt.Sprintf("%s/api/sources/agent/session/validate", strings.TrimRight(s.backendURL, "/"))
	payload, _ := json.Marshal(map[string]string{
		"token":       token,
		"source_id":   sourceID.String(),
		"agent_id":    s.agentID,
		"folder_name": folderName,
		"source_path": sourcePath,
	})
	req, err := http.NewRequest(http.MethodPost, validateURL, bytes.NewReader(payload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("failed to notify backend session binding", "error", err, "backend", s.backendURL)
		} else {
			resp.Body.Close()
		}
	}
}

func (s *AgentServer) registerDirectory(rawPath, token string) (*LocalSourceSession, error) {
	canonicalPath, err := s.pathValidator.ValidateSourcePath(rawPath)
	if err != nil {
		return nil, err
	}

	sourceID := uuid.New()
	folderName := filepath.Base(canonicalPath)
	if folderName == "" || folderName == "." || folderName == "/" {
		folderName = "local-project"
	}

	// Avoid repeatedly scanning the same repository if a recent completed analysis exists
	s.mu.RLock()
	var cachedAnalysis *analyzer.AnalysisResult
	for _, sess := range s.sessions {
		if sess.CanonicalPath == canonicalPath && (sess.Status == "ready" || sess.Status == "consumed") && sess.Analysis != nil && time.Since(sess.UpdatedAt) < 10*time.Minute {
			cachedAnalysis = sess.Analysis
			break
		}
	}
	s.mu.RUnlock()

	if cachedAnalysis != nil {
		session := &LocalSourceSession{
			SourceID:      sourceID,
			CanonicalPath: canonicalPath,
			FolderName:    folderName,
			Status:        "ready",
			Phase:         "ready",
			TotalFiles:    cachedAnalysis.TotalFiles,
			TotalBytes:    cachedAnalysis.TotalBytes,
			DetectedCount: len(cachedAnalysis.Services),
			Analysis:      cachedAnalysis,
			Discovery:     cachedAnalysis.Discovery,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
			ExpiresAt:     time.Now().Add(s.sessionTTL),
			Token:         token,
		}
		s.mu.Lock()
		s.sessions[sourceID] = session
		s.mu.Unlock()
		RegisterLocalSourceSession(session)

		if token != "" {
			if sm := GetGlobalSessionManager(); sm != nil {
				_, _ = sm.BindSourceWithPath(token, sourceID, folderName, s.agentID, canonicalPath)
			}
			s.notifyBackendSession(token, sourceID, folderName, canonicalPath)
		}
		return session, nil
	}

	session := &LocalSourceSession{
		SourceID:      sourceID,
		CanonicalPath: canonicalPath,
		FolderName:    folderName,
		Status:        "created",
		Phase:         "scanning",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(s.sessionTTL),
		Token:         token,
	}

	s.mu.Lock()
	s.sessions[sourceID] = session
	s.mu.Unlock()
	RegisterLocalSourceSession(session)

	// Bind source and folder on backend session
	if token != "" {
		if sm := GetGlobalSessionManager(); sm != nil {
			_, _ = sm.BindSourceWithPath(token, sourceID, folderName, s.agentID, canonicalPath)
		}
		s.notifyBackendSession(token, sourceID, folderName, canonicalPath)
	}

	// Channel to signal quick completion
	doneCh := make(chan struct{})

	go func() {
		analysis, err := analyzer.AnalyzeRepositoryWithProgress(canonicalPath, func(phase string, filesScanned, totalFiles, detectedCount int) {
			session.mu.Lock()
			session.Phase = phase
			if phase == "scanning" || phase == "detecting" {
				session.Status = phase
			}
			session.FilesScanned = filesScanned
			if totalFiles > 0 {
				session.TotalFiles = totalFiles
			}
			session.DetectedCount = detectedCount
			session.UpdatedAt = time.Now()
			session.mu.Unlock()
		})

		session.mu.Lock()
		if err != nil {
			session.Status = "failed"
			session.Phase = "failed"
			session.Error = err.Error()
			slog.Error("local agent analysis failed", "source_id", sourceID, "error", err)
		} else {
			session.Status = "ready"
			session.Phase = "ready"
			session.TotalFiles = analysis.TotalFiles
			session.TotalBytes = analysis.TotalBytes
			session.DetectedCount = len(analysis.Services)
			session.Analysis = analysis
			if analysis.Discovery != nil {
				session.Discovery = analysis.Discovery
			}
			slog.Info("registered local source session",
				"source_id", sourceID.String(),
				"folder_name", folderName,
				"services_count", len(analysis.Services),
				"total_files", analysis.TotalFiles,
			)
		}
		session.UpdatedAt = time.Now()
		session.mu.Unlock()
		close(doneCh)
	}()

	// Grace window: if analysis finishes quickly (< 1000ms), return ready immediately;
	// otherwise return initial scanning session and let frontend poll
	select {
	case <-doneCh:
	case <-time.After(1000 * time.Millisecond):
	}

	return session, nil
}

func (s *AgentServer) renderSessionResponse(w http.ResponseWriter, session *LocalSourceSession) {
	session.mu.RLock()
	defer session.mu.RUnlock()

	totalFiles := session.TotalFiles
	totalBytes := session.TotalBytes
	var services []analyzer.ServiceDefinition
	if session.Analysis != nil {
		totalFiles = session.Analysis.TotalFiles
		totalBytes = session.Analysis.TotalBytes
		services = session.Analysis.Services
	}
	if services == nil {
		services = []analyzer.ServiceDefinition{}
	}

	status := session.Status
	phase := session.Phase
	if status != "consumed" {
		if session.Analysis != nil || len(services) > 0 {
			status = "ready"
			phase = "ready"
		}
		if status == "" {
			if session.Error != "" {
				status = "failed"
				phase = "failed"
			} else {
				status = "scanning"
				phase = "scanning"
			}
		}
	}

	resp := map[string]interface{}{
		"source": map[string]interface{}{
			"id":               session.SourceID.String(),
			"source_type":      "local_agent",
			"source_reference": session.SourceID.String(),
			"agent_id":         s.agentID,
			"folder_name":      session.FolderName,
			"status":           status,
			"phase":            phase,
			"total_files":      totalFiles,
			"total_bytes":      totalBytes,
		},
		"source_id":      session.SourceID.String(),
		"agent_id":       s.agentID,
		"folder_name":    session.FolderName,
		"status":         status,
		"phase":          phase,
		"files_scanned":  session.FilesScanned,
		"total_files":    totalFiles,
		"total_bytes":    totalBytes,
		"detected_count": session.DetectedCount,
		"services":       services,
		"registered_at":  session.CreatedAt.Format(time.RFC3339),
	}

	if session.Error != "" {
		resp["error"] = session.Error
	}

	disc := session.Discovery
	if disc == nil && session.Analysis != nil {
		disc = session.Analysis.Discovery
	}
	if disc == nil && session.CanonicalPath != "" && (session.Status == "ready" || session.Status == "consumed") {
		if d, err := discovery.Discover(session.CanonicalPath); err == nil {
			disc = d
			session.Discovery = d
		}
	}
	if disc != nil {
		resp["discovery"] = disc
	}

	writeJSON(w, http.StatusOK, resp)
}
