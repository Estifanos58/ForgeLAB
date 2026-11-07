package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
)

var (
	ErrDeploymentNotFound            = errors.New("deployment not found")
	ErrNoDeploymentToRollback        = errors.New("no previous deployment to rollback to")
	ErrCannotCancelRunningDeployment = errors.New("cannot cancel a deployment that is already running")
	ErrDeploymentAlreadyTerminal     = errors.New("deployment is already in a terminal state")
	ErrProjectDeleting               = errors.New("cannot perform deployment: project deletion is in progress")
)

// DeploymentService handles deployment-related business logic.
type DeploymentService struct {
	db            *pgxpool.Pool
	secretService *SecretService
	githubService *GitHubService
}

// NewDeploymentService creates a new DeploymentService.
func NewDeploymentService(db *pgxpool.Pool) *DeploymentService {
	return &DeploymentService{db: db}
}

// SetSecretService sets the SecretService instance for env hash calculations.
func (s *DeploymentService) SetSecretService(sec *SecretService) {
	s.secretService = sec
}

// SetGitHubService sets the GitHubService instance for commit SHA resolution.
func (s *DeploymentService) SetGitHubService(gh *GitHubService) {
	s.githubService = gh
}

// resolveSourceRevision computes an immutable source revision:
//   - GitHub commit SHA if available (pinned 40-character SHA), otherwise resolves via githubService
//   - For local directories: fresh content fingerprint from directory if accessible on disk.
//     If source files have changed after plan review, invalidates the plan and returns an explicit error.
//   - For local/agent/upload sources: deterministic source fingerprint from sources table or plan SourceRevision
//
// Fails explicitly if fingerprinting fails.
func (s *DeploymentService) resolveSourceRevision(ctx context.Context, project *models.Project, commitSHA *string) (*string, error) {
	if project == nil {
		return nil, nil
	}

	if project.SourceType == models.SourceTypeGitHub || project.SourceType == models.SourceTypeGitHubPublic {
		var targetSHA string
		if commitSHA != nil && len(*commitSHA) == 40 {
			targetSHA = *commitSHA
		}
		if targetSHA == "" && s.githubService != nil {
			repoRef := project.SourceReference
			if repoRef == "" {
				repoRef = project.RepositoryPath
			}
			parts := strings.Split(repoRef, "/")
			if len(parts) >= 2 {
				owner, repo := parts[0], parts[1]
				branch := project.Branch
				if branch == "" {
					branch = "main"
				}
				if resolvedSHA, err := s.githubService.ResolveCommitSHA(ctx, project.OwnerID, owner, repo, branch); err == nil && len(resolvedSHA) == 40 {
					targetSHA = resolvedSHA
				}
			}
		}
		if targetSHA == "" && project.SourceID != nil && s.db != nil {
			var fp string
			var metaBytes []byte
			if err := s.db.QueryRow(ctx, "SELECT fingerprint, metadata FROM sources WHERE id = $1", *project.SourceID).Scan(&fp, &metaBytes); err == nil {
				if len(fp) == 40 {
					targetSHA = fp
				} else if len(metaBytes) > 0 {
					var meta map[string]interface{}
					if err := json.Unmarshal(metaBytes, &meta); err == nil {
						if csha, ok := meta["commit_sha"].(string); ok && len(csha) == 40 {
							targetSHA = csha
						}
					}
				}
			}
		}
		if targetSHA == "" {
			// Check if project has an authoritative DeploymentPlan with a pinned 40-character commit SHA
			if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
				var plan struct {
					SourceRevision string `json:"source_revision"`
				}
				if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil {
					rev := strings.TrimSpace(plan.SourceRevision)
					if len(rev) == 40 {
						targetSHA = rev
					}
				}
			}
		}

		if targetSHA != "" {
			if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
				var plan struct {
					SourceRevision string `json:"source_revision"`
				}
				if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
					if plan.SourceRevision != targetSHA {
						if s.db != nil {
							_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
						}
						project.DeploymentPlan = []byte("{}")
						return nil, fmt.Errorf("remote GitHub repository revision changed after deployment plan review (reviewed: %s, latest: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, targetSHA)
					}
				}
			}
			return &targetSHA, nil
		}
		// Do not return mutable branch strings for GitHub deployments to guarantee reproducibility
		return nil, fmt.Errorf("failed to pin immutable 40-character commit SHA for GitHub deployment of project '%s'", project.Name)
	}

	// For local directory: if the directory exists on disk, compute fresh content fingerprint.
	// Ensure that its fingerprint, discovery results, service topology, reviewed deployment plan,
	// cache key, and resulting image all correspond to exactly the same content.
	if (project.SourceType == models.SourceTypeLocalDirectory || project.SourceType == models.SourceTypeLocal) && project.RepositoryPath != "" {
		if fi, err := os.Stat(project.RepositoryPath); err == nil && fi.IsDir() {
			fp, fpErr := discovery.ComputeDirectoryContentFingerprint(project.RepositoryPath)
			if fpErr != nil {
				return nil, fmt.Errorf("failed to compute source directory fingerprint for %q: %w", project.RepositoryPath, fpErr)
			}
			if fp != "" {
				// If source files change after review, invalidate the plan and require fresh discovery/review rather than updating only source_revision
				if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
					var plan struct {
						SourceRevision string `json:"source_revision"`
					}
					if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
						if plan.SourceRevision != fp {
							if s.db != nil {
								_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
							}
							project.DeploymentPlan = []byte("{}")
							return nil, fmt.Errorf("source files changed after deployment plan review (reviewed revision: %s, current revision: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, fp)
						}
					}
				}
				return &fp, nil
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to access source directory %q: %w", project.RepositoryPath, err)
		}
	}

	// Check if project has an authoritative DeploymentPlan with SourceRevision (e.g. offline mock or pre-verified plan)
	if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
		var plan struct {
			SourceRevision string `json:"source_revision"`
		}
		if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && strings.TrimSpace(plan.SourceRevision) != "" {
			rev := strings.TrimSpace(plan.SourceRevision)
			return &rev, nil
		}
	}

	// For local_agent, local_upload, or local_directory: check if source has an immutable fingerprint in database
	if s.db != nil {
		var srcID uuid.UUID
		if project.SourceID != nil && *project.SourceID != uuid.Nil {
			srcID = *project.SourceID
		} else if parsed, err := uuid.Parse(project.SourceReference); err == nil {
			srcID = parsed
		}
		if srcID != uuid.Nil {
			var fingerprint string
			err := s.db.QueryRow(ctx, "SELECT fingerprint FROM sources WHERE id = $1", srcID).Scan(&fingerprint)
			if err == nil && strings.TrimSpace(fingerprint) != "" {
				fp := strings.TrimSpace(fingerprint)
				if len(project.DeploymentPlan) > 0 && string(project.DeploymentPlan) != "{}" {
					var plan struct {
						SourceRevision string `json:"source_revision"`
					}
					if err := json.Unmarshal(project.DeploymentPlan, &plan); err == nil && plan.SourceRevision != "" {
						if plan.SourceRevision != fp {
							_, _ = s.db.Exec(ctx, "UPDATE projects SET deployment_plan = '{}'::jsonb WHERE id = $1", project.ID)
							project.DeploymentPlan = []byte("{}")
							return nil, fmt.Errorf("source files changed after deployment plan review (reviewed revision: %s, current revision: %s); deployment plan has been invalidated, please rediscover and review the plan", plan.SourceRevision, fp)
						}
					}
				}
				return &fp, nil
			}
		}
	}

	// Fallback to deterministic fingerprint of source reference
	ref := project.SourceReference
	if ref == "" {
		ref = project.RepositoryPath
	}
	if ref != "" {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", project.SourceType, ref)))
		fp := fmt.Sprintf("fp_%s", hex.EncodeToString(h[:16]))
		return &fp, nil
	}

	return nil, nil
}

func resolvePublicHost() string {
	publicHost := os.Getenv("FORGELAB_PUBLIC_HOST")
	if publicHost == "" {
		publicHost = os.Getenv("PUBLIC_HOST")
	}
	if publicHost == "" {
		publicHost = "localhost"
	}
	return publicHost
}
