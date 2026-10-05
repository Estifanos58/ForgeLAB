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
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        COALESCE(classification, 'application'), COALESCE(image, ''), COALESCE(depends_on, '[]'::jsonb),
		        COALESCE(volumes, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb)
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
		var candidatesJSON, dependsOnJSON, volumesJSON, hcJSON []byte

		err := rows.Scan(
			&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
			&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &candidatesJSON, &svc.BuildCommand,
			&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
			&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
			&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
			&svc.CreatedAt, &svc.UpdatedAt,
			&svc.CpuMillicores, &svc.MemoryMB, &svc.PidsLimit, &svc.EphemeralStorageMB,
			&svc.Classification, &svc.Image, &dependsOnJSON, &volumesJSON, &hcJSON,
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
		if len(dependsOnJSON) > 0 {
			_ = json.Unmarshal(dependsOnJSON, &svc.DependsOn)
		}
		if svc.DependsOn == nil {
			svc.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			_ = json.Unmarshal(volumesJSON, &svc.Volumes)
		}
		if svc.Volumes == nil {
			svc.Volumes = []models.VolumeMountConfig{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			_ = json.Unmarshal(hcJSON, &svc.HealthCheckConfig)
		}

		populateServicePreviewURL(svc)
		services = append(services, svc)
	}

	if services == nil {
		services = []*models.Service{}
	}

	return services, nil
}

// ListServicesByProjectIDs retrieves services for multiple projects in a single batch query (avoiding N+1).
func (s *ServiceService) ListServicesByProjectIDs(ctx context.Context, projectIDs []uuid.UUID) (map[uuid.UUID][]*models.Service, error) {
	result := make(map[uuid.UUID][]*models.Service)
	if s.db == nil || len(projectIDs) == 0 {
		return result, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        COALESCE(classification, 'application'), COALESCE(image, ''), COALESCE(depends_on, '[]'::jsonb),
		        COALESCE(volumes, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb)
		 FROM services WHERE project_id = ANY($1) ORDER BY created_at ASC`,
		projectIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to batch query services: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		svc := &models.Service{}
		var candidatesJSON, dependsOnJSON, volumesJSON, hcJSON []byte

		err := rows.Scan(
			&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
			&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &candidatesJSON, &svc.BuildCommand,
			&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
			&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
			&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
			&svc.CreatedAt, &svc.UpdatedAt,
			&svc.CpuMillicores, &svc.MemoryMB, &svc.PidsLimit, &svc.EphemeralStorageMB,
			&svc.Classification, &svc.Image, &dependsOnJSON, &volumesJSON, &hcJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan batch service: %w", err)
		}

		if len(candidatesJSON) > 0 {
			_ = json.Unmarshal(candidatesJSON, &svc.BuildCandidates)
		}
		if svc.BuildCandidates == nil {
			svc.BuildCandidates = []models.BuildCandidate{}
		}
		if len(dependsOnJSON) > 0 {
			_ = json.Unmarshal(dependsOnJSON, &svc.DependsOn)
		}
		if svc.DependsOn == nil {
			svc.DependsOn = []string{}
		}
		if len(volumesJSON) > 0 {
			_ = json.Unmarshal(volumesJSON, &svc.Volumes)
		}
		if svc.Volumes == nil {
			svc.Volumes = []models.VolumeMountConfig{}
		}
		if len(hcJSON) > 0 && string(hcJSON) != "{}" {
			_ = json.Unmarshal(hcJSON, &svc.HealthCheckConfig)
		}

		populateServicePreviewURL(svc)
		result[svc.ProjectID] = append(result[svc.ProjectID], svc)
	}

	return result, rows.Err()
}

// ListActiveServices retrieves all services that are currently marked as running or deploying, or have an assigned container.
func (s *ServiceService) ListActiveServices(ctx context.Context) ([]*models.Service, error) {
	if s.db == nil {
		return []*models.Service{}, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb
		 FROM services WHERE status IN ('running', 'deploying', 'starting', 'health_checking') OR container_id IS NOT NULL ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query active services: %w", err)
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
			&svc.CpuMillicores, &svc.MemoryMB, &svc.PidsLimit, &svc.EphemeralStorageMB,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan active service: %w", err)
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
	return services, rows.Err()
}

// GetService retrieves a service by ID.
func (s *ServiceService) GetService(ctx context.Context, serviceID uuid.UUID) (*models.Service, error) {
	if s.db == nil {
		return nil, ErrServiceNotFound
	}

	svc := &models.Service{}
	var candidatesJSON, dependsOnJSON, volumesJSON, hcJSON []byte

	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
		        build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
		        internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
		        status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
		        cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
		        COALESCE(classification, 'application'), COALESCE(image, ''), COALESCE(depends_on, '[]'::jsonb),
		        COALESCE(volumes, '[]'::jsonb), COALESCE(healthcheck_config, '{}'::jsonb)
		 FROM services WHERE id = $1`,
		serviceID,
	).Scan(
		&svc.ID, &svc.ProjectID, &svc.SourceID, &svc.Name, &svc.Role, &svc.SourcePath, &svc.RuntimeType,
		&svc.Framework, &svc.PackageManager, &svc.BuildStrategy, &candidatesJSON, &svc.BuildCommand,
		&svc.StartCommand, &svc.DockerfilePath, &svc.BuildContext, &svc.InternalPort, &svc.HostPort,
		&svc.PublicExposed, &svc.HealthStrategy, &svc.HealthCheckPath, &svc.HealthCheckEnabled,
		&svc.Status, &svc.ContainerID, &svc.ImageTag, &svc.CurrentServiceDeploymentID,
		&svc.CreatedAt, &svc.UpdatedAt,
		&svc.CpuMillicores, &svc.MemoryMB, &svc.PidsLimit, &svc.EphemeralStorageMB,
		&svc.Classification, &svc.Image, &dependsOnJSON, &volumesJSON, &hcJSON,
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
	if len(dependsOnJSON) > 0 {
		_ = json.Unmarshal(dependsOnJSON, &svc.DependsOn)
	}
	if svc.DependsOn == nil {
		svc.DependsOn = []string{}
	}
	if len(volumesJSON) > 0 {
		_ = json.Unmarshal(volumesJSON, &svc.Volumes)
	}
	if svc.Volumes == nil {
		svc.Volumes = []models.VolumeMountConfig{}
	}
	if len(hcJSON) > 0 && string(hcJSON) != "{}" {
		_ = json.Unmarshal(hcJSON, &svc.HealthCheckConfig)
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

	if svc.CpuMillicores <= 0 {
		svc.CpuMillicores = 1000
	}
	if svc.MemoryMB <= 0 {
		svc.MemoryMB = 1024
	}
	if svc.PidsLimit <= 0 {
		svc.PidsLimit = 256
	}
	if svc.Classification == "" {
		svc.Classification = models.ClassificationApplication
	}

	candidatesJSON, _ := json.Marshal(svc.BuildCandidates)
	if len(candidatesJSON) == 0 {
		candidatesJSON = []byte("[]")
	}
	dependsOnJSON, _ := json.Marshal(svc.DependsOn)
	if len(dependsOnJSON) == 0 {
		dependsOnJSON = []byte("[]")
	}
	volumesJSON, _ := json.Marshal(svc.Volumes)
	if len(volumesJSON) == 0 {
		volumesJSON = []byte("[]")
	}
	var hcJSON []byte
	if svc.HealthCheckConfig != nil {
		hcJSON, _ = json.Marshal(svc.HealthCheckConfig)
	}
	if len(hcJSON) == 0 {
		hcJSON = []byte("{}")
	}

	_, err := s.db.Exec(ctx,
		`INSERT INTO services (
			id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
			build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
			internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
			status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			classification, image, depends_on, volumes, healthcheck_config
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26, $27,
			$28, $29, $30, $31,
			$32, $33, $34, $35, $36
		)`,
		svc.ID, svc.ProjectID, svc.SourceID, svc.Name, svc.Role, svc.SourcePath, svc.RuntimeType,
		svc.Framework, svc.PackageManager, svc.BuildStrategy, candidatesJSON, svc.BuildCommand,
		svc.StartCommand, svc.DockerfilePath, svc.BuildContext, svc.InternalPort, svc.HostPort,
		svc.PublicExposed, svc.HealthStrategy, svc.HealthCheckPath, svc.HealthCheckEnabled,
		svc.Status, svc.ContainerID, svc.ImageTag, svc.CurrentServiceDeploymentID,
		svc.CreatedAt, svc.UpdatedAt,
		svc.CpuMillicores, svc.MemoryMB, svc.PidsLimit, svc.EphemeralStorageMB,
		svc.Classification, svc.Image, dependsOnJSON, volumesJSON, hcJSON,
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

	if svc.CpuMillicores <= 0 {
		svc.CpuMillicores = 1000
	}
	if svc.MemoryMB <= 0 {
		svc.MemoryMB = 1024
	}
	if svc.PidsLimit <= 0 {
		svc.PidsLimit = 256
	}
	if svc.Classification == "" {
		svc.Classification = models.ClassificationApplication
	}

	candidatesJSON, _ := json.Marshal(svc.BuildCandidates)
	if len(candidatesJSON) == 0 {
		candidatesJSON = []byte("[]")
	}
	dependsOnJSON, _ := json.Marshal(svc.DependsOn)
	if len(dependsOnJSON) == 0 {
		dependsOnJSON = []byte("[]")
	}
	volumesJSON, _ := json.Marshal(svc.Volumes)
	if len(volumesJSON) == 0 {
		volumesJSON = []byte("[]")
	}
	var hcJSON []byte
	if svc.HealthCheckConfig != nil {
		hcJSON, _ = json.Marshal(svc.HealthCheckConfig)
	}
	if len(hcJSON) == 0 {
		hcJSON = []byte("{}")
	}

	_, err := tx.Exec(ctx,
		`INSERT INTO services (
			id, project_id, source_id, name, role, source_path, runtime_type, framework, package_manager,
			build_strategy, build_candidates, build_command, start_command, dockerfile_path, build_context,
			internal_port, host_port, public_exposed, health_strategy, health_check_path, health_check_enabled,
			status, container_id, image_tag, current_service_deployment_id, created_at, updated_at,
			cpu_millicores, memory_mb, pids_limit, ephemeral_storage_mb,
			classification, image, depends_on, volumes, healthcheck_config
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21,
			$22, $23, $24, $25, $26, $27,
			$28, $29, $30, $31,
			$32, $33, $34, $35, $36
		)`,
		svc.ID, svc.ProjectID, svc.SourceID, svc.Name, svc.Role, svc.SourcePath, svc.RuntimeType,
		svc.Framework, svc.PackageManager, svc.BuildStrategy, candidatesJSON, svc.BuildCommand,
		svc.StartCommand, svc.DockerfilePath, svc.BuildContext, svc.InternalPort, svc.HostPort,
		svc.PublicExposed, svc.HealthStrategy, svc.HealthCheckPath, svc.HealthCheckEnabled,
		svc.Status, svc.ContainerID, svc.ImageTag, svc.CurrentServiceDeploymentID,
		svc.CreatedAt, svc.UpdatedAt,
		svc.CpuMillicores, svc.MemoryMB, svc.PidsLimit, svc.EphemeralStorageMB,
		svc.Classification, svc.Image, dependsOnJSON, volumesJSON, hcJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to insert service in tx: %w", err)
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
		serviceID, models.ServiceStatusRunning, serviceDeploymentID, containerID, imageTag, hostPort,
	)
	if err != nil {
		return fmt.Errorf("failed to promote service deployment: %w", err)
	}

	return nil
}

// CalculateProjectStatus derives overall project state from individual service coarse statuses.
// Valid coarse service statuses: ServiceStatusInactive, ServiceStatusDeploying, ServiceStatusRunning, ServiceStatusStopped, ServiceStatusFailed.
func CalculateProjectStatus(services []*models.Service) string {
	if len(services) == 0 {
		return models.ProjectStatusInactive
	}

	var runningCount, failedCount, stoppedCount, deployingCount, inactiveCount int

	for _, svc := range services {
		switch svc.Status {
		case models.ServiceStatusRunning:
			runningCount++
		case models.ServiceStatusDeploying:
			deployingCount++
		case models.ServiceStatusFailed:
			failedCount++
		case models.ServiceStatusStopped:
			stoppedCount++
		case models.ServiceStatusInactive:
			inactiveCount++
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
