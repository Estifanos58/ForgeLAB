package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

var (
	ErrServiceNotFound = errors.New("service not found")
)

type ServiceService struct {
	db *pgxpool.Pool
}

func NewServiceService(db *pgxpool.Pool) *ServiceService {
	return &ServiceService{db: db}
}

// ListServices retrieves all services configured for a project.
func (s *ServiceService) ListServices(ctx context.Context, projectID uuid.UUID) ([]*models.Service, error) {
	if s.db == nil {
		return []*models.Service{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at
		 FROM services WHERE project_id = $1 ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query services: %w", err)
	}
	defer rows.Close()

	var services []*models.Service
	for rows.Next() {
		svc := &models.Service{}
		var candidatesJSON []byte

		err := rows.Scan(
			&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
			&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &candidatesJSON, &svc.BuildCommand,
			&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
			&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
			&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
			&svc.CreatedAt, &svc.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service: %w", err)
		}

		if len(candidatesJSON) > 0 {
			_ = json.Unmarshal(candidatesJSON, &svc.BuildCandidates)
		}
		if svc.BuildCandidates == nil {
			svc.BuildCandidates = []models.BuildCandidate{}
		}

		populateServicePreviewURL(svc)
		services = append(services, svc)
	}

	if services == nil {
		services = []*models.Service{}
	}

	return services, nil
}

// GetService retrieves a service by ID.
func (s *ServiceService) GetService(ctx context.Context, serviceID uuid.UUID) (*models.Service, error) {
	if s.db == nil {
		return nil, ErrServiceNotFound
	}

	svc := &models.Service{}
	var candidatesJSON []byte

	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at
		 FROM services WHERE id = $1`,
		serviceID,
	).Scan(
		&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
		&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &candidatesJSON, &svc.BuildCommand,
		&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
		&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
		&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
		&svc.CreatedAt, &svc.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrServiceNotFound
		}
		return nil, fmt.Errorf("failed to query service: %w", err)
	}

	if len(candidatesJSON) > 0 {
		_ = json.Unmarshal(candidatesJSON, &svc.BuildCandidates)
	}
	if svc.BuildCandidates == nil {
		svc.BuildCandidates = []models.BuildCandidate{}
	}

	populateServicePreviewURL(svc)
	return svc, nil
}

// GetServiceByID is an alias for GetService satisfying the websocket.ServiceResolver interface.
func (s *ServiceService) GetServiceByID(ctx context.Context, serviceID uuid.UUID) (*models.Service, error) {
	return s.GetService(ctx, serviceID)
}

func populateServicePreviewURL(svc *models.Service) {
	if svc == nil {
		return
	}
	if svc.PublicExposed && svc.HostPort != nil && *svc.HostPort > 0 {
		host := os.Getenv("FORGELAB_PUBLIC_HOST")
		if host == "" {
			host = os.Getenv("PUBLIC_HOST")
		}
		if host == "" {
			host = "localhost"
		}
		url := fmt.Sprintf("http://%s:%d", host, *svc.HostPort)
		svc.PreviewURL = &url
	}
}

// CreateService inserts a new service.
func (s *ServiceService) CreateService(ctx context.Context, svc *models.Service) error {
	if s.db == nil {
		return nil
	}

	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	now := time.Now()
	svc.CreatedAt = now
	svc.UpdatedAt = now

	candidatesJSON, _ := json.Marshal(svc.BuildCandidates)
	if len(candidatesJSON) == 0 {
		candidatesJSON = []byte("[]")
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO services (
			id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
			build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
			internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
			status, container_id, image_tag, current_service_deployment_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26, $27
		)`,
		svc.ID, svc.ProjectID, svc.SourceID, svc.Name, svc.Role, svc.SourcePath, svc.RuntimeType,
		svc.Framework, svc.PackageManager, svc.BuildStrategy, candidatesJSON, svc.BuildCommand,
		svc.StartCommand, svc.DockerfilePath, svc.BuildContext, svc.InternalPort, svc.HostPort,
		svc.PublicExposed, svc.HealthStrategy, svc.HealthCheckPath, svc.HealthCheckEnabled,
		svc.Status, svc.ContainerID, svc.ImageTag, svc.CurrentServiceDeploymentID,
		svc.CreatedAt, svc.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert service: %w", err)
	}

	slog.Info("service created", "service_id", svc.ID, "project_id", svc.ProjectID, "name", svc.Name)
	return nil
}

// CreateServiceTx inserts a new service inside an active database transaction.
func (s *ServiceService) CreateServiceTx(ctx context.Context, tx pgx.Tx, svc *models.Service) error {
	if tx == nil {
		return s.CreateService(ctx, svc)
	}

	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	now := time.Now()
	svc.CreatedAt = now
	svc.UpdatedAt = now

	candidatesJSON, _ := json.Marshal(svc.BuildCandidates)
	if len(candidatesJSON) == 0 {
		candidatesJSON = []byte("[]")
	}

	_, err := tx.Exec(ctx,
		`INSERT INTO services (
			id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
			build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
			internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
			status, container_id, image_tag, current_service_deployment_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26, $27
		)`,
		svc.ID, svc.ProjectID, svc.SourceID, svc.Name, svc.Role, svc.SourcePath, svc.RuntimeType,
		svc.Framework, svc.PackageManager, svc.BuildStrategy, candidatesJSON, svc.BuildCommand,
		svc.StartCommand, svc.DockerfilePath, svc.BuildContext, svc.InternalPort, svc.HostPort,
		svc.PublicExposed, svc.HealthStrategy, svc.HealthCheckPath, svc.HealthCheckEnabled,
		svc.Status, svc.ContainerID, svc.ImageTag, svc.CurrentServiceDeploymentID,
		svc.CreatedAt, svc.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert service: %w", err)
	}

	slog.Info("service created in tx", "service_id", svc.ID, "project_id", svc.ProjectID, "name", svc.Name)
	return nil
}

// UpdateServiceStatus updates a service's status and runtime container/port fields.
func (s *ServiceService) UpdateServiceStatus(
	ctx context.Context,
	serviceID uuid.UUID,
	status string,
	containerID *string,
	imageTag *string,
	hostPort *int,
) error {
	if s.db == nil {
		return nil
	}

	_, err := s.db.Exec(ctx,
		`UPDATE services SET
		 status = $2,
		 container_id = COALESCE($3, container_id),
		 image_tag = COALESCE($4, image_tag),
		 host_port = COALESCE($5, host_port),
		 updated_at = NOW()
		 WHERE id = $1`,
		serviceID, status, containerID, imageTag, hostPort,
	)
	if err != nil {
		return fmt.Errorf("failed to update service status: %w", err)
	}

	return nil
}

// PromoteServiceDeployment updates the service record after a successful service deployment.
func (s *ServiceService) PromoteServiceDeployment(
	ctx context.Context,
	serviceID uuid.UUID,
	serviceDeploymentID uuid.UUID,
	containerID string,
	imageTag string,
	hostPort *int,
) error {
	if s.db == nil {
		return nil
	}

	_, err := s.db.Exec(ctx,
		`UPDATE services SET
		 status = $2,
		 current_service_deployment_id = $3,
		 container_id = $4,
		 image_tag = $5,
		 host_port = $6,
		 updated_at = NOW()
		 WHERE id = $1`,
		serviceID, models.DeployStatusRunning, serviceDeploymentID, containerID, imageTag, hostPort,
	)
	if err != nil {
		return fmt.Errorf("failed to promote service deployment: %w", err)
	}

	return nil
}

// CalculateProjectStatus derives overall project state from individual service coarse statuses.
func CalculateProjectStatus(services []*models.Service) string {
	if len(services) == 0 {
		return models.ProjectStatusInactive
	}

	var runningCount, failedCount, stoppedCount, deployingCount, inactiveCount int

	for _, svc := range services {
		switch {
		case svc.Status == models.ProjectStatusRunning:
			runningCount++
		case svc.Status == models.ProjectStatusDeploying ||
			models.IsDeploymentActiveStatus(svc.Status):
			// Count queued, cloning, building, starting, health_checking as deploying
			deployingCount++
		case svc.Status == models.ProjectStatusFailed || svc.Status == models.DeployStatusCrashed:
			failedCount++
		case svc.Status == models.ProjectStatusStopped:
			stoppedCount++
		default:
			inactiveCount++
		}
	}

	// 1. Any deploying service makes the project deploying
	if deployingCount > 0 {
		return models.ProjectStatusDeploying
	}

	// 2. All running -> running
	if runningCount == len(services) {
		return models.ProjectStatusRunning
	}

	// 3. Partial running (some running, others stopped or failed)
	if runningCount > 0 {
		return models.ProjectStatusPartiallyRunning
	}

	// 4. Any failed and none running -> failed
	if failedCount > 0 {
		return models.ProjectStatusFailed
	}

	// 5. All stopped -> stopped
	if stoppedCount == len(services) {
		return models.ProjectStatusStopped
	}

	// 6. Inactive default
	if inactiveCount == len(services) {
		return models.ProjectStatusInactive
	}

	return models.ProjectStatusInactive
}

// UpdateServiceResources updates the configurable resource limits for a service.
func (s *ServiceService) UpdateServiceResources(ctx context.Context, serviceID uuid.UUID, cfg models.ResourceConfig) error {
	if s.db == nil {
		return nil
	}

	_, err := s.db.Exec(ctx,
		`UPDATE services SET
		 cpu_millicores = $2,
		 memory_mb = $3,
		 pids_limit = $4,
		 ephemeral_storage_mb = $5,
		 updated_at = NOW()
		 WHERE id = $1`,
		serviceID, cfg.CpuMillicores, cfg.MemoryMB, cfg.PidsLimit, cfg.EphemeralStorageMB,
	)
	if err != nil {
		return fmt.Errorf("failed to update service resources: %w", err)
	}

	slog.Info("service resources updated", "service_id", serviceID, "cpu", cfg.CpuMillicores, "mem", cfg.MemoryMB, "pids", cfg.PidsLimit)
	return nil
}

