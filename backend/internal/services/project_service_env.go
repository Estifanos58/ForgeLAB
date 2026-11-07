package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/agent"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/envparser"
	"github.com/forgelab/backend/internal/models"
)

func (s *ProjectService) ImportProjectEnvironment(ctx context.Context, project *models.Project, ownerID uuid.UUID) (int, error) {
	if s.secretService == nil {
		return 0, nil
	}

	var entries []ImportEnvVarEntry
	seen := make(map[string]bool)

	addEntry := func(svcID *uuid.UUID, key, val string) {
		trimmedKey := strings.TrimSpace(key)
		trimmedVal := strings.TrimSpace(val)
		if trimmedKey == "" || trimmedVal == "" || trimmedVal == "[REDACTED]" {
			return
		}
		svcKey := "project"
		if svcID != nil {
			svcKey = svcID.String()
		}
		compKey := svcKey + ":" + trimmedKey
		if !seen[compKey] {
			seen[compKey] = true
			entries = append(entries, ImportEnvVarEntry{
				ServiceID: svcID,
				Key:       trimmedKey,
				Value:     trimmedVal,
				Scope:     models.EnvScopeRuntime,
			})
		}
	}

	if project.SourceType == models.SourceTypeLocalAgent && project.SourceReference != "" {
		sourceUUID, err := uuid.Parse(project.SourceReference)
		if err != nil {
			return 0, fmt.Errorf("invalid source UUID: %w", err)
		}

		var agentToken string
		if s.sourceService != nil {
			agentToken, _ = s.sourceService.GetDecryptedAgentToken(ctx, ownerID, sourceUUID)
		}

		candidateBases := agent.ResolveCandidateBaseURLs()
		var serviceQueryParts []string
		for _, svc := range project.Services {
			if svc.SourcePath != "" && svc.SourcePath != "." {
				serviceQueryParts = append(serviceQueryParts, "service_path="+url.QueryEscape(svc.SourcePath))
			}
		}

		client := &http.Client{Timeout: 5 * time.Second}
		for _, baseURL := range candidateBases {
			agentURL := fmt.Sprintf("%s/api/agent/sources/%s/environment", baseURL, project.SourceReference)
			if len(serviceQueryParts) > 0 {
				agentURL += "?" + strings.Join(serviceQueryParts, "&")
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURL, nil)
			if err != nil {
				continue
			}
			if agentToken != "" {
				req.Header.Set("Authorization", "Bearer "+agentToken)
				req.Header.Set("X-Agent-Session-Token", agentToken)
			}

			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var envResp struct {
						Root     map[string]string            `json:"root"`
						Services map[string]map[string]string `json:"services"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&envResp); err == nil {
						// Service-specific .env and Compose declarations first (higher precedence)
						for _, svc := range project.Services {
							svcID := svc.ID
							if svcEnv, ok := envResp.Services[svc.Name]; ok {
								for k, v := range svcEnv {
									addEntry(&svcID, k, v)
								}
							}
							if svc.SourcePath != "" && svc.SourcePath != "." {
								if svcEnv, ok := envResp.Services[svc.SourcePath]; ok {
									for k, v := range svcEnv {
										addEntry(&svcID, k, v)
									}
								}
							}
						}
						// Root .env mapped to project-level
						for k, v := range envResp.Root {
							addEntry(nil, k, v)
						}
						break
					}
				}
			}
		}
	} else {
		// Non-agent sources: local directory, uploaded archive, or GitHub repository
		var workspaceDir string
		var cleanupDir func()

		switch project.SourceType {
		case models.SourceTypeLocalDirectory, models.SourceTypeLocal:
			workspaceDir = project.RepositoryPath

		case models.SourceTypeLocalUpload:
			if s.sourceService != nil && project.SourceReference != "" {
				if sourceUUID, err := uuid.Parse(project.SourceReference); err == nil {
					p, err := s.sourceService.GetSourcePath(ctx, ownerID, sourceUUID)
					if err == nil {
						workspaceDir = p
					}
				}
			}

		case models.SourceTypeGitHub, models.SourceTypeGitHubPublic:
			if project.SourceID != nil && s.sourceService != nil {
				if src, err := s.sourceService.GetSourceByID(ctx, *project.SourceID); err == nil && src != nil && src.Metadata != nil {
					if snapVal, ok := src.Metadata["snapshot_path"].(string); ok && snapVal != "" {
						if info, err := os.Stat(snapVal); err == nil && info.IsDir() {
							workspaceDir = snapVal
						}
					}
				}
			}
			if workspaceDir == "" && s.githubService != nil && project.SourceReference != "" {
				parts := strings.Split(project.SourceReference, "/")
				if len(parts) == 2 {
					tempDir, err := os.MkdirTemp("", "forgelab-env-import-*")
					if err == nil {
						cleanupDir = func() { _ = os.RemoveAll(tempDir) }
						ref := project.Branch
						if ref == "" {
							ref = "main"
						}
						_, matErr := s.githubService.MaterializeGitHubSnapshot(ctx, ownerID, parts[0], parts[1], ref, tempDir)
						if matErr == nil {
							workspaceDir = tempDir
						}
					}
				}
			}
		}

		if cleanupDir != nil {
			defer cleanupDir()
		}

		if workspaceDir != "" {
			// 1. Check for Docker Compose definition in workspaceDir or under project.BuildContext
			if composePath, found := discovery.FindComposeFile(workspaceDir); found {
				if _, composeSvcs, err := discovery.ParseComposeFile(composePath, workspaceDir); err == nil {
					// Map compose service environment to matching project services
					for _, svc := range project.Services {
						for _, cs := range composeSvcs {
							if cs.Name == svc.Name {
								svcID := svc.ID
								for _, envDef := range cs.Environment {
									val := envDef.RawValue
									if val == "" && envDef.Value != "[REDACTED]" {
										val = envDef.Value
									}
									if val != "" && val != "[REDACTED]" {
										addEntry(&svcID, envDef.Key, val)
									}
								}
								break
							}
						}
					}
				}
			} else if project.BuildContext != "" && project.BuildContext != "." {
				cleanSub := filepath.Clean(filepath.FromSlash(project.BuildContext))
				subComposeDir := filepath.Join(workspaceDir, cleanSub)
				if subComposePath, found := discovery.FindComposeFile(subComposeDir); found {
					if _, composeSvcs, err := discovery.ParseComposeFile(subComposePath, subComposeDir); err == nil {
						for _, svc := range project.Services {
							for _, cs := range composeSvcs {
								if cs.Name == svc.Name {
									svcID := svc.ID
									for _, envDef := range cs.Environment {
										val := envDef.RawValue
										if val == "" && envDef.Value != "[REDACTED]" {
											val = envDef.Value
										}
										if val != "" && val != "[REDACTED]" {
											addEntry(&svcID, envDef.Key, val)
										}
									}
									break
								}
							}
						}
					}
				}
			}

			// 2. Service-specific .env files
			for _, svc := range project.Services {
				if svc.SourcePath != "" {
					cleanRel := filepath.Clean(filepath.FromSlash(svc.SourcePath))
					if !strings.HasPrefix(cleanRel, "..") {
						var candDirs []string
						candDirs = append(candDirs, filepath.Join(workspaceDir, cleanRel))
						if project.BuildContext != "" && project.BuildContext != "." {
							candDirs = append(candDirs, filepath.Join(workspaceDir, filepath.Clean(filepath.FromSlash(project.BuildContext)), cleanRel))
						}
						for _, dir := range candDirs {
							svcEnvPath := filepath.Join(dir, ".env")
							if svcVars, err := envparser.ParseFile(svcEnvPath); err == nil && len(svcVars) > 0 {
								svcID := svc.ID
								for k, v := range svcVars {
									addEntry(&svcID, k, v)
								}
								break
							}
						}
					}
				}
			}

			// 3. Root .env file
			var rootEnvCandidates []string
			rootEnvCandidates = append(rootEnvCandidates, filepath.Join(workspaceDir, ".env"))
			if project.BuildContext != "" && project.BuildContext != "." {
				rootEnvCandidates = append(rootEnvCandidates, filepath.Join(workspaceDir, filepath.Clean(filepath.FromSlash(project.BuildContext)), ".env"))
			}
			for _, rPath := range rootEnvCandidates {
				if rootVars, err := envparser.ParseFile(rPath); err == nil && len(rootVars) > 0 {
					for k, v := range rootVars {
						addEntry(nil, k, v)
					}
					break
				}
			}
		}
	}

	if len(entries) == 0 {
		return 0, nil
	}

	return s.secretService.ImportEnvVarsIfMissing(ctx, project.ID, ownerID, entries)
}
