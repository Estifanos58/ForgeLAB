package services

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

// UpdateProjectStatus updates the overall status of a project.
func (s *ProjectService) UpdateProjectStatus(ctx context.Context, projectID uuid.UUID, status string) error {
	if s.updateProjectStatusForTest != nil {
		return s.updateProjectStatusForTest(ctx, projectID, status)
	}
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(ctx, "UPDATE projects SET status = $1, updated_at = NOW() WHERE id = $2", status, projectID)
	return err
}

// UpdateProjectPort updates the assigned host port for a project.
func (s *ProjectService) UpdateProjectPort(ctx context.Context, projectID uuid.UUID, port int) (*models.Project, error) {
	if s.db == nil {
		p, err := s.GetProjectByIDWithoutOwnership(ctx, projectID)
		if p != nil {
			p.Port = &port
		}
		return p, err
	}
	_, err := s.db.Exec(ctx,
		"UPDATE projects SET port = $1, updated_at = $2 WHERE id = $3",
		port, time.Now(), projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update project port: %w", err)
	}
	return s.GetProjectByIDWithoutOwnership(ctx, projectID)
}

// ResolveAuthoritativeHost returns the public host name or IP used for preview URLs.
func ResolveAuthoritativeHost() string {
	if host := os.Getenv("FORGELAB_PUBLIC_HOST"); host != "" {
		return host
	}
	if host := os.Getenv("PUBLIC_HOST"); host != "" {
		return host
	}
	if frontendURL := os.Getenv("FRONTEND_URL"); frontendURL != "" {
		if u, err := url.Parse(frontendURL); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return "localhost"
}

func (s *ProjectService) resolveAuthoritativeHost() string {
	return ResolveAuthoritativeHost()
}

func (s *ProjectService) PopulateProjectPreviewURL(p *models.Project) {
	if p == nil {
		return
	}
	publicHost := s.resolveAuthoritativeHost()

	var primarySvcURL *string
	var frontendSvcURL *string

	for _, svc := range p.Services {
		// Only running public services have an active live preview URL
		isInternal := svc.Classification == models.ClassificationInfrastructure ||
			svc.Classification == models.ClassificationWorker ||
			svc.Classification == models.ClassificationJob
		isFrontend := !isInternal && (svc.Role == models.RoleFrontend || strings.Contains(strings.ToLower(svc.Name), "frontend"))
		isPublic := !isInternal && (svc.PublicExposed || isFrontend)
		if (svc.Status == models.ServiceStatusRunning || svc.Status == models.ServiceStatusDegraded) && svc.HostPort != nil && *svc.HostPort > 0 && isPublic {
			urlStr := fmt.Sprintf("http://%s:%d", publicHost, *svc.HostPort)
			svc.PreviewURL = &urlStr

			if isFrontend && frontendSvcURL == nil {
				frontendSvcURL = &urlStr
			} else if primarySvcURL == nil {
				primarySvcURL = &urlStr
			}
		} else {
			svc.PreviewURL = nil
		}
	}

	if frontendSvcURL != nil {
		p.PreviewURL = frontendSvcURL
	} else if primarySvcURL != nil {
		p.PreviewURL = primarySvcURL
	} else if len(p.Services) == 0 && (p.Status == models.ProjectStatusRunning || p.Status == models.ProjectStatusDegraded || p.Status == models.ProjectStatusPartiallyRunning) && p.Port != nil && *p.Port > 0 {
		urlStr := fmt.Sprintf("http://%s:%d", publicHost, *p.Port)
		p.PreviewURL = &urlStr
	} else {
		p.PreviewURL = nil
	}
}

func (s *ProjectService) populateProjectPreviewURL(p *models.Project) {
	s.PopulateProjectPreviewURL(p)
}
