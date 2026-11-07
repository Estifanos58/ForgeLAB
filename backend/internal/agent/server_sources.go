package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/analyzer"
	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/dockerignore"
	"github.com/forgelab/backend/internal/envparser"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/tararchive"
)

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
		if globalSess, ok := LookupLocalSourceSession(sourceUUID); ok {
			s.mu.Lock()
			s.sessions[sourceUUID] = globalSess
			s.mu.Unlock()
			session = globalSess
			exists = true
		}
	}

	if !exists {
		slog.Warn("agent source session not found in local memory",
			"source_id", sourceUUID.String(),
			"agent_id", s.agentID,
		)
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "source session not found or expired",
			"reason": "session not found",
		})
		return
	}

	// Extend session TTL on active access
	session.ExpiresAt = time.Now().Add(s.sessionTTL)

	// Verify token match
	token, _ := r.Context().Value("agent_token").(string)
	if session.Token == "" && token != "" {
		session.Token = token
	} else if session.Token != "" && token != "" && session.Token != token {
		slog.Warn("agent source session token mismatch",
			"source_id", sourceUUID.String(),
			"agent_id", s.agentID,
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden: token does not match source session",
			"reason": "source mismatch",
		})
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
			DeleteLocalSourceSession(sourceUUID)
			writeJSON(w, http.StatusOK, map[string]string{"message": "session removed"})
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := parts[1]
	switch action {
	case "consume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		session.Status = "consumed"
		session.Phase = "consumed"
		session.UpdatedAt = time.Now()
		s.mu.Unlock()
		s.renderSessionResponse(w, session)

	case "discovery":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		session.mu.RLock()
		disc := session.Discovery
		if disc == nil && session.Analysis != nil {
			disc = session.Analysis.Discovery
		}
		status := session.Status
		canonPath := session.CanonicalPath
		sessErr := session.Error
		session.mu.RUnlock()

		if status == "failed" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": fmt.Sprintf("source analysis failed: %s", sessErr),
			})
			return
		}

		if disc == nil && canonPath != "" {
			var err error
			disc, err = discovery.Discover(canonPath)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": fmt.Sprintf("discovery failed: %v", err),
				})
				return
			}
			session.mu.Lock()
			session.Discovery = disc
			session.mu.Unlock()
		}

		if disc == nil {
			writeJSON(w, http.StatusAccepted, map[string]string{
				"status":  status,
				"message": "discovery in progress",
			})
			return
		}

		writeJSON(w, http.StatusOK, disc)
		return

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
		if analysis.Discovery != nil {
			session.Discovery = analysis.Discovery
		}
		session.UpdatedAt = time.Now()
		s.mu.Unlock()
		s.renderSessionResponse(w, session)

	case "path", "source-path":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		session.mu.RLock()
		canonPath := session.CanonicalPath
		session.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]string{
			"source_id":   sourceUUID.String(),
			"source_path": canonPath,
		})
		return

	case "environment", "env":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSourceEnvironment(w, r, session)

	case "stream-context":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		serviceRelPath := r.URL.Query().Get("service_path")
		cleanRelPath, pathErr := security.ValidateRelativeServicePath(serviceRelPath)
		if pathErr != nil {
			http.Error(w, fmt.Sprintf("invalid service path traversal: %v", pathErr), http.StatusForbidden)
			return
		}

		serviceDir := filepath.Join(session.CanonicalPath, filepath.FromSlash(cleanRelPath))
		evalServiceDir, err := filepath.EvalSymlinks(serviceDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid service directory: %v", err), http.StatusBadRequest)
			return
		}
		relCheck, err := filepath.Rel(session.CanonicalPath, evalServiceDir)
		if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
			http.Error(w, "invalid service path traversal", http.StatusForbidden)
			return
		}

		fi, err := os.Stat(evalServiceDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("service directory not accessible: %v", err), http.StatusBadRequest)
			return
		}
		if !fi.IsDir() {
			http.Error(w, "service path is not a directory", http.StatusBadRequest)
			return
		}

		// Virtual files setup
		virtualFiles := make(map[string][]byte)
		expectedDockerfile := r.URL.Query().Get("dockerfile")
		genRuntime := r.URL.Query().Get("runtime")
		if expectedDockerfile != "" {
			// Specific Dockerfile requested (e.g. models.BuildStrategyDockerfile)
			// Do not generate synthetic Dockerfile; preserve project's real Dockerfile.
		} else if genRuntime != "" {
			genPort := 8080
			if p, err := strconv.Atoi(r.URL.Query().Get("port")); err == nil && p > 0 {
				genPort = p
			}
			genStartCmd := r.URL.Query().Get("start_cmd")
			genPkgManager := r.URL.Query().Get("package_manager")
			if genPkgManager == "" {
				genPkgManager = r.URL.Query().Get("pkg_manager")
			}
			if genPkgManager == "" {
				// Inspect evalServiceDir for lockfiles
				if _, err := os.Stat(filepath.Join(evalServiceDir, "pnpm-lock.yaml")); err == nil {
					genPkgManager = "pnpm"
				} else if _, err := os.Stat(filepath.Join(evalServiceDir, "yarn.lock")); err == nil {
					genPkgManager = "yarn"
				} else if _, err := os.Stat(filepath.Join(evalServiceDir, "bun.lockb")); err == nil {
					genPkgManager = "bun"
				} else if _, err := os.Stat(filepath.Join(evalServiceDir, "bun.lock")); err == nil {
					genPkgManager = "bun"
				} else if _, err := os.Stat(filepath.Join(evalServiceDir, "package-lock.json")); err == nil {
					genPkgManager = "npm"
				} else if _, err := os.Stat(filepath.Join(evalServiceDir, "npm-shrinkwrap.json")); err == nil {
					genPkgManager = "npm"
				} else if pkgRaw, err := os.ReadFile(filepath.Join(evalServiceDir, "package.json")); err == nil {
					var pkg struct {
						PackageManager string `json:"packageManager"`
					}
					if err := json.Unmarshal(pkgRaw, &pkg); err == nil && pkg.PackageManager != "" {
						pmDecl := strings.ToLower(pkg.PackageManager)
						if strings.HasPrefix(pmDecl, "pnpm") {
							genPkgManager = "pnpm"
						} else if strings.HasPrefix(pmDecl, "yarn") {
							genPkgManager = "yarn"
						} else if strings.HasPrefix(pmDecl, "bun") {
							genPkgManager = "bun"
						} else if strings.HasPrefix(pmDecl, "npm") {
							genPkgManager = "npm"
						}
					}
				}
			}
			var prismaInfo *discovery.PrismaStagingInfo
			if evalServiceDir != "" {
				prismaInfo, _ = detector.DetectPrismaFromDir(evalServiceDir)
			}
			dockerfileContent := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
				Runtime:        genRuntime,
				Port:           genPort,
				StartCmd:       genStartCmd,
				PackageManager: genPkgManager,
				Prisma:         prismaInfo,
			})
			virtualFiles["Dockerfile.forgelab"] = []byte(dockerfileContent)
			expectedDockerfile = "Dockerfile.forgelab"
		} else {
			if _, statErr := os.Stat(filepath.Join(evalServiceDir, "Dockerfile")); statErr == nil {
				expectedDockerfile = "Dockerfile"
			}
		}

		matcher, dErr := dockerignore.LoadDockerignore(evalServiceDir)
		if dErr != nil {
			slog.Error("error loading .dockerignore", "source_id", session.SourceID, "error", dErr)
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "failed to read or parse .dockerignore",
				"reason": dErr.Error(),
			})
			return
		}

		bOpts := tararchive.BuildContextOptions{
			BuildContextDir: evalServiceDir,
			Matcher:         matcher,
			VirtualFiles:    virtualFiles,
		}

		// Generate completed, validated archive to disk first before sending any HTTP response headers
		tmpFile, err := tararchive.CreateCompletedArchiveFile(r.Context(), os.TempDir(), bOpts, expectedDockerfile)
		if err != nil {
			slog.Error("error creating agent build context archive", "source_id", session.SourceID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":  "failed to generate build context archive",
				"reason": err.Error(),
			})
			return
		}
		defer func() {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
		}()

		stat, err := tmpFile.Stat()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":  "failed to stat build context archive",
				"reason": err.Error(),
			})
			return
		}

		// Compute SHA256 of completed archive for integrity verification
		h := sha256.New()
		if _, hashErr := io.Copy(h, tmpFile); hashErr == nil {
			archiveSha := hex.EncodeToString(h.Sum(nil))
			w.Header().Set("X-Archive-SHA256", archiveSha)
		}
		_, _ = tmpFile.Seek(0, io.SeekStart)

		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		w.Header().Set("X-Archive-Total-Bytes", strconv.FormatInt(stat.Size(), 10))
		w.WriteHeader(http.StatusOK)

		if _, copyErr := io.Copy(w, tmpFile); copyErr != nil {
			slog.Error("error streaming completed archive to client", "source_id", session.SourceID, "error", copyErr)
		}

	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// SourceEnvironmentResponse represents the parsed .env variables returned to the authenticated backend.
type SourceEnvironmentResponse struct {
	SourceID uuid.UUID                    `json:"source_id"`
	Root     map[string]string            `json:"root"`
	Services map[string]map[string]string `json:"services"`
}

func (s *AgentServer) handleSourceEnvironment(w http.ResponseWriter, r *http.Request, session *LocalSourceSession) {
	resp := SourceEnvironmentResponse{
		SourceID: session.SourceID,
		Root:     make(map[string]string),
		Services: make(map[string]map[string]string),
	}

	// 1. Compose file declarations (docker-compose.yml / compose.yaml)
	if composePath, found := discovery.FindComposeFile(session.CanonicalPath); found {
		if _, composeSvcs, err := discovery.ParseComposeFile(composePath, session.CanonicalPath); err == nil {
			for _, cs := range composeSvcs {
				if len(cs.Environment) > 0 {
					if _, ok := resp.Services[cs.Name]; !ok {
						resp.Services[cs.Name] = make(map[string]string)
					}
					for _, envDef := range cs.Environment {
						val := envDef.RawValue
						if val == "" && envDef.Value != "[REDACTED]" {
							val = envDef.Value
						}
						if val != "" && val != "[REDACTED]" {
							resp.Services[cs.Name][envDef.Key] = val
						}
					}
					if cs.SourcePath != "" && cs.SourcePath != "." {
						if _, ok := resp.Services[cs.SourcePath]; !ok {
							resp.Services[cs.SourcePath] = make(map[string]string)
						}
						for _, envDef := range cs.Environment {
							val := envDef.RawValue
							if val == "" && envDef.Value != "[REDACTED]" {
								val = envDef.Value
							}
							if val != "" && val != "[REDACTED]" {
								resp.Services[cs.SourcePath][envDef.Key] = val
							}
						}
					}
				}
			}
		} else {
			slog.Warn("error parsing compose file for environment", "source_id", session.SourceID, "path", composePath, "error", err)
		}
	}

	// 2. Root .env
	rootEnvPath := filepath.Join(session.CanonicalPath, ".env")
	if fi, err := os.Stat(rootEnvPath); err == nil && !fi.IsDir() {
		if rootVars, err := envparser.ParseFile(rootEnvPath); err == nil {
			resp.Root = rootVars
		} else {
			slog.Warn("error parsing root .env", "source_id", session.SourceID, "error", err)
		}
	}

	// 3. Service-specific .env
	servicePaths := r.URL.Query()["service_path"]
	if len(servicePaths) == 0 {
		if sp := r.URL.Query().Get("service_paths"); sp != "" {
			servicePaths = strings.Split(sp, ",")
		} else if sp := r.URL.Query().Get("services"); sp != "" {
			servicePaths = strings.Split(sp, ",")
		}
	}

	if len(servicePaths) > 0 {
		for _, sp := range servicePaths {
			trimmed := strings.TrimSpace(sp)
			if trimmed == "" || trimmed == "." {
				continue
			}
			cleanRel, err := security.ValidateRelativeServicePath(trimmed)
			if err != nil {
				continue
			}
			svcDir := filepath.Join(session.CanonicalPath, filepath.FromSlash(cleanRel))
			evalSvcDir, err := filepath.EvalSymlinks(svcDir)
			if err != nil {
				continue
			}
			relCheck, err := filepath.Rel(session.CanonicalPath, evalSvcDir)
			if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
				continue
			}

			svcEnvPath := filepath.Join(evalSvcDir, ".env")
			if fi, err := os.Stat(svcEnvPath); err == nil && !fi.IsDir() {
				if svcVars, err := envparser.ParseFile(svcEnvPath); err == nil && len(svcVars) > 0 {
					resp.Services[cleanRel] = svcVars
				}
			}
		}
	} else {
		// Discover immediate subdirectories under root for .env
		entries, err := os.ReadDir(session.CanonicalPath)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || analyzer.IsPrunedDir(entry.Name()) {
					continue
				}
				subDir := filepath.Join(session.CanonicalPath, entry.Name())
				evalSubDir, err := filepath.EvalSymlinks(subDir)
				if err != nil {
					continue
				}
				relCheck, err := filepath.Rel(session.CanonicalPath, evalSubDir)
				if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
					continue
				}
				subEnvPath := filepath.Join(evalSubDir, ".env")
				if fi, err := os.Stat(subEnvPath); err == nil && !fi.IsDir() {
					if subVars, err := envparser.ParseFile(subEnvPath); err == nil && len(subVars) > 0 {
						resp.Services[entry.Name()] = subVars
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
