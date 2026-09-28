package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/docker"
)

// LocalSourceSession stores the in-memory mapping between an opaque source ID and the local host path
type LocalSourceSession struct {
	SourceID      uuid.UUID                `json:"source_id"`
	CanonicalPath string                   `json:"-"` // Never exposed over HTTP/JSON
	FolderName    string                   `json:"folder_name"`
	Analysis      *analyzer.AnalysisResult `json:"analysis"`
	CreatedAt     time.Time                `json:"created_at"`
}

type AgentServerConfig struct {
	Port         int
	AllowedRoots []string
	BackendURL   string
}

type AgentServer struct {
	agentID       string
	port          int
	backendURL    string
	pathValidator *PathValidator
	picker        *NativeFolderPicker
	sessions      map[uuid.UUID]*LocalSourceSession
	mu            sync.RWMutex
	dockerClient  *client.Client
}

func NewAgentServer(cfg AgentServerConfig) *AgentServer {
	if cfg.Port <= 0 {
		cfg.Port = 4142
	}
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())

	return &AgentServer{
		agentID:       uuid.New().String(),
		port:          cfg.Port,
		backendURL:    cfg.BackendURL,
		pathValidator: NewPathValidator(cfg.AllowedRoots),
		picker:        NewNativeFolderPicker(),
		sessions:      make(map[uuid.UUID]*LocalSourceSession),
		dockerClient:  cli,
	}
}

// Router returns an http.Handler with all agent endpoints and CORS support
func (s *AgentServer) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/status", s.handleStatus)
	mux.HandleFunc("/api/agent/select-folder", s.handleSelectFolder)
	mux.HandleFunc("/api/agent/select-path", s.handleSelectPath)
	mux.HandleFunc("/api/agent/sources/", s.handleSourcesRoutes)

	return s.corsMiddleware(mux)
}

func (s *AgentServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-ForgeLAB-Agent")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *AgentServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dockerAvailable := false
	if s.dockerClient != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if _, err := s.dockerClient.Ping(ctx); err == nil {
			dockerAvailable = true
		}
	}

	s.mu.RLock()
	sessionCount := len(s.sessions)
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "online",
		"agent_id":         s.agentID,
		"version":          "1.0.0",
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"docker_available": dockerAvailable,
		"active_sessions":  sessionCount,
	})
}

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
		if err == ErrPickerCancelled {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"cancelled": true,
			})
			return
		}
		http.Error(w, fmt.Sprintf("Failed to open native folder picker: %v", err), http.StatusInternalServerError)
		return
	}

	session, err := s.registerDirectory(selectedPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to inspect selected directory: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

// handleSelectPath directly validates and analyzes a path (e.g. for CLI/automated testing)
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

	session, err := s.registerDirectory(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Path validation failed: %v", err), http.StatusBadRequest)
		return
	}

	s.renderSessionResponse(w, session)
}

func (s *AgentServer) registerDirectory(rawPath string) (*LocalSourceSession, error) {
	canonicalPath, err := s.pathValidator.ValidateSourcePath(rawPath)
	if err != nil {
		return nil, err
	}

	analysis, err := analyzer.AnalyzeRepository(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze repository structure: %w", err)
	}

	sourceID := uuid.New()
	folderName := filepath.Base(canonicalPath)
	if folderName == "" || folderName == "." || folderName == "/" {
		folderName = "local-project"
	}

	session := &LocalSourceSession{
		SourceID:      sourceID,
		CanonicalPath: canonicalPath,
		FolderName:    folderName,
		Analysis:      analysis,
		CreatedAt:     time.Now(),
	}

	s.mu.Lock()
	s.sessions[sourceID] = session
	s.mu.Unlock()

	slog.Info("registered local source session",
		"source_id", sourceID.String(),
		"folder_name", folderName,
		"services_count", len(analysis.Services),
	)

	return session, nil
}

func (s *AgentServer) renderSessionResponse(w http.ResponseWriter, session *LocalSourceSession) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source": map[string]interface{}{
			"id":               session.SourceID.String(),
			"source_type":      "local_agent",
			"source_reference": session.SourceID.String(),
			"agent_id":         s.agentID,
			"folder_name":      session.FolderName,
			"total_files":      session.Analysis.TotalFiles,
			"total_bytes":      session.Analysis.TotalBytes,
		},
		"source_id":     session.SourceID.String(),
		"agent_id":      s.agentID,
		"folder_name":   session.FolderName,
		"total_files":   session.Analysis.TotalFiles,
		"total_bytes":   session.Analysis.TotalBytes,
		"services":      session.Analysis.Services,
		"registered_at": session.CreatedAt.Format(time.RFC3339),
	})
}

func (s *AgentServer) handleSourcesRoutes(w http.ResponseWriter, r *http.Request) {
	// Subpaths:
	// GET /api/agent/sources/:source_id
	// DELETE /api/agent/sources/:source_id
	// POST /api/agent/sources/:source_id/analyze
	// GET /api/agent/sources/:source_id/stream-context
	path := strings.TrimPrefix(r.URL.Path, "/api/agent/sources/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "source_id is required", http.StatusBadRequest)
		return
	}

	sourceUUID, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid source UUID format", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	session, exists := s.sessions[sourceUUID]
	s.mu.RUnlock()

	if !exists {
		http.Error(w, "source session not found or expired", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			s.renderSessionResponse(w, session)
			return
		}
		if r.Method == http.MethodDelete {
			s.mu.Lock()
			delete(s.sessions, sourceUUID)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"message": "session removed"})
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := parts[1]
	switch action {
	case "analyze":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		analysis, err := analyzer.AnalyzeRepository(session.CanonicalPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("re-analysis failed: %v", err), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		session.Analysis = analysis
		s.mu.Unlock()
		s.renderSessionResponse(w, session)

	case "stream-context":
		// Streams tarball of a service's source directory (respects .dockerignore)
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		serviceRelPath := r.URL.Query().Get("service_path")
		if serviceRelPath == "" {
			serviceRelPath = "."
		}

		serviceDir := filepath.Join(session.CanonicalPath, filepath.FromSlash(serviceRelPath))
		if !strings.HasPrefix(filepath.Clean(serviceDir), session.CanonicalPath) {
			http.Error(w, "invalid service path traversal", http.StatusForbidden)
			return
		}

		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Transfer-Encoding", "chunked")

		tarWriter := tar.NewWriter(w)
		defer tarWriter.Close()

		matcher, _ := docker.LoadDockerignore(serviceDir)

		_ = filepath.WalkDir(serviceDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(serviceDir, p)
			if err != nil || rel == "." || rel == "" {
				return nil
			}
			slashRel := filepath.ToSlash(rel)
			isDir := d.IsDir()

			if analyzer.IsPrunedDir(d.Name()) || analyzer.IsSecretFile(d.Name()) {
				if isDir {
					return filepath.SkipDir
				}
				return nil
			}

			if matcher != nil {
				if isDir && matcher.CanSkipDir(slashRel) {
					return filepath.SkipDir
				}
				if matcher.Matches(slashRel, isDir) {
					if isDir {
						return filepath.SkipDir
					}
					return nil
				}
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return nil
			}
			header.Name = slashRel
			if isDir {
				header.Name += "/"
			}

			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}

			if !isDir && info.Mode().IsRegular() {
				f, err := os.Open(p)
				if err != nil {
					return nil
				}
				_, _ = io.Copy(tarWriter, f)
				f.Close()
			}
			return nil
		})

		// If runtime is specified for auto build, inject virtual Dockerfile.forgelab into the stream
		genRuntime := r.URL.Query().Get("runtime")
		if genRuntime != "" {
			genPort := 8080
			if p, err := strconv.Atoi(r.URL.Query().Get("port")); err == nil && p > 0 {
				genPort = p
			}
			genStartCmd := r.URL.Query().Get("start_cmd")
			dockerfileContent := detector.GenerateDockerfile(genRuntime, genPort, genStartCmd)

			header := &tar.Header{
				Name:    "Dockerfile.forgelab",
				Mode:    0644,
				Size:    int64(len(dockerfileContent)),
				ModTime: time.Now(),
			}
			if err := tarWriter.WriteHeader(header); err == nil {
				_, _ = tarWriter.Write([]byte(dockerfileContent))
			}
		}

	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
