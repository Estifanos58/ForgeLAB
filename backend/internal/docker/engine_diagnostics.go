package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/services"
)

const (
	highMemoryWarningThresholdPct  = 85.0
	largeReclaimableThresholdBytes = 5 * 1024 * 1024 * 1024 // 5 GB
	staleImportThreshold           = 5 * time.Minute
)

// DockerConnectivityDiagnostics captures Docker daemon reachability, API metadata, and reconciliation state.
type DockerConnectivityDiagnostics struct {
	Available        bool                `json:"available"`
	PingLatencyMs    int64               `json:"ping_latency_ms"`
	APIVersion       string              `json:"api_version,omitempty"`
	ServerVersion    string              `json:"server_version,omitempty"`
	OperatingSystem  string              `json:"operating_system,omitempty"`
	OSType           string              `json:"os_type,omitempty"`
	Architecture     string              `json:"architecture,omitempty"`
	StorageDriver    string              `json:"storage_driver,omitempty"`
	BuilderVersion   string              `json:"builder_version,omitempty"`
	CPUs             int                 `json:"cpus,omitempty"`
	MemoryTotalBytes int64               `json:"memory_total_bytes,omitempty"`
	DaemonWarnings   []string            `json:"daemon_warnings,omitempty"`
	Error            string              `json:"error,omitempty"`
	Reconciliation   ReconciliationState `json:"reconciliation"`
}

// DeploymentFailureDiagnostic describes a failed or crashed release or service deployment.
type DeploymentFailureDiagnostic struct {
	Scope         string     `json:"scope"` // "release" or "service"
	ID            uuid.UUID  `json:"id"`
	ProjectID     uuid.UUID  `json:"project_id"`
	ProjectName   string     `json:"project_name"`
	ServiceID     *uuid.UUID `json:"service_id,omitempty"`
	ServiceName   string     `json:"service_name,omitempty"`
	DeployNumber  int        `json:"deploy_number"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failure_reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// ServiceResourceWarning highlights container memory pressure, OOM kills, restart loops, or health failures.
type ServiceResourceWarning struct {
	ProjectID           uuid.UUID `json:"project_id"`
	ProjectName         string    `json:"project_name"`
	ServiceID           uuid.UUID `json:"service_id"`
	ServiceName         string    `json:"service_name"`
	Status              string    `json:"status"`
	CPUPercent          float64   `json:"cpu_percent"`
	MemoryUsedBytes     uint64    `json:"memory_used_bytes"`
	MemoryLimitBytes    uint64    `json:"memory_limit_bytes"`
	MemoryPercent       float64   `json:"memory_percent"`
	OOMKilled           bool      `json:"oom_killed"`
	RestartCount        int       `json:"restart_count"`
	HealthStatus        string    `json:"health_status"`
	HealthFailingStreak int       `json:"health_failing_streak"`
	SelfHealRestarts    int       `json:"self_heal_restarts,omitempty"`
	CrashLoopEvents     int       `json:"crash_loop_events,omitempty"`
	Reason              string    `json:"reason"`
}

// ResourceDiagnostic summarizes host/daemon capacity, Docker disk usage, and service resource pressure.
type ResourceDiagnostic struct {
	DockerAvailable      bool                     `json:"docker_available"`
	HostCPUs             int                      `json:"host_cpus,omitempty"`
	HostMemoryTotalBytes int64                    `json:"host_memory_total_bytes,omitempty"`
	DiskUsage            DiskUsageSummary         `json:"disk_usage"`
	ExhaustedServices    []ServiceResourceWarning `json:"exhausted_services"`
}

// ImportDiagnostic captures repository import failures or stalled jobs.
type ImportDiagnostic struct {
	ImportID     uuid.UUID  `json:"import_id"`
	Repository   string     `json:"repository"`
	Branch       string     `json:"branch"`
	Status       string     `json:"status"`
	Stage        string     `json:"stage,omitempty"`
	Progress     int        `json:"progress"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	Stale        bool       `json:"stale"`
	ProjectID    *uuid.UUID `json:"project_id,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// DiagnosticIssue is an actionable operational finding with severity and remediation guidance.
type DiagnosticIssue struct {
	Severity    string `json:"severity"` // "critical", "warning", "info"
	Category    string `json:"category"` // "docker_connectivity", "deployment_failure", "resource_exhaustion", "repository_import", "runtime_health"
	Title       string `json:"title"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
	ResourceID  string `json:"resource_id,omitempty"`
}

// DiagnosticsReport aggregates operational health across Docker connectivity, deployments, resources, and imports.
type DiagnosticsReport struct {
	Scope              string                        `json:"scope"` // "system" or "project"
	ProjectID          *uuid.UUID                    `json:"project_id,omitempty"`
	ProjectName        string                        `json:"project_name,omitempty"`
	OverallStatus      string                        `json:"overall_status"` // "healthy", "degraded", "critical"
	CollectedAt        time.Time                     `json:"collected_at"`
	Docker             DockerConnectivityDiagnostics `json:"docker"`
	DeploymentFailures []DeploymentFailureDiagnostic `json:"deployment_failures"`
	Resources          ResourceDiagnostic            `json:"resources"`
	Imports            []ImportDiagnostic            `json:"imports"`
	Issues             []DiagnosticIssue             `json:"issues"`
}

// GetSystemDiagnostics builds an operational diagnostics report across all projects owned by ownerID.
func (e *Engine) GetSystemDiagnostics(
	ctx context.Context,
	ownerID uuid.UUID,
	importSvc *services.RepositoryImportService,
) (*DiagnosticsReport, error) {
	var projects []*models.Project
	if e.projectService != nil && ownerID != uuid.Nil {
		list, err := e.projectService.ListProjects(ctx, ownerID, 100, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list projects for diagnostics: %w", err)
		}
		projects = list
	}
	return e.buildDiagnosticsReport(ctx, "system", nil, ownerID, projects, importSvc), nil
}

// GetProjectDiagnostics builds a project-scoped operational diagnostics report.
func (e *Engine) GetProjectDiagnostics(
	ctx context.Context,
	projectID uuid.UUID,
	importSvc *services.RepositoryImportService,
) (*DiagnosticsReport, error) {
	if e.projectService == nil {
		return nil, fmt.Errorf("project service unavailable")
	}
	project, err := e.getProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pid := project.ID
	return e.buildDiagnosticsReport(ctx, "project", &pid, project.OwnerID, []*models.Project{project}, importSvc), nil
}

func (e *Engine) buildDiagnosticsReport(
	ctx context.Context,
	scope string,
	projectID *uuid.UUID,
	ownerID uuid.UUID,
	projects []*models.Project,
	importSvc *services.RepositoryImportService,
) *DiagnosticsReport {
	now := time.Now().UTC()
	report := &DiagnosticsReport{
		Scope:              scope,
		ProjectID:          projectID,
		OverallStatus:      "healthy",
		CollectedAt:        now,
		DeploymentFailures: []DeploymentFailureDiagnostic{},
		Resources: ResourceDiagnostic{
			ExhaustedServices: []ServiceResourceWarning{},
		},
		Imports: []ImportDiagnostic{},
		Issues:  []DiagnosticIssue{},
	}
	if len(projects) == 1 && projectID != nil {
		report.ProjectName = projects[0].Name
	}

	// 1. Docker Connectivity & Reconciliation
	dockerDiag := e.collectDockerConnectivity(ctx)
	report.Docker = dockerDiag
	report.Resources.DockerAvailable = dockerDiag.Available
	report.Resources.HostCPUs = dockerDiag.CPUs
	report.Resources.HostMemoryTotalBytes = dockerDiag.MemoryTotalBytes

	if !dockerDiag.Available {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Severity:    "critical",
			Category:    "docker_connectivity",
			Title:       "Docker Daemon Unreachable",
			Message:     dockerDiag.Error,
			Remediation: "Ensure Docker Engine / Docker Desktop is running and the mounted Docker socket is accessible to the ForgeLAB backend.",
		})
	} else {
		if !dockerDiag.Reconciliation.Authoritative && dockerDiag.Reconciliation.Status != "" {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "warning",
				Category:    "docker_connectivity",
				Title:       fmt.Sprintf("Daemon Reconciliation %s", dockerDiag.Reconciliation.Status),
				Message:     firstNonEmptyDiag(dockerDiag.Reconciliation.LastError, "Docker-to-database state reconciliation has not completed authoritatively."),
				Remediation: "Wait for the reconciliation pass to finish or verify Docker daemon health in /ready.",
			})
		}
		for _, w := range dockerDiag.DaemonWarnings {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "warning",
				Category:    "docker_connectivity",
				Title:       "Docker Daemon Warning",
				Message:     w,
				Remediation: "Review Docker daemon configuration and host kernel/storage prerequisites.",
			})
		}
	}

	// 2. Host/Daemon Disk Usage
	if dockerDiag.Available {
		diskUsage, _, dfErr := e.collectDockerDiskUsage(ctx)
		report.Resources.DiskUsage = diskUsage
		if dfErr != nil {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "warning",
				Category:    "resource_exhaustion",
				Title:       "Docker Disk Usage Collection Failed",
				Message:     diskUsage.Warning,
				Remediation: "Check Docker daemon responsiveness and storage driver health.",
			})
		} else if diskUsage.TotalReclaimable >= largeReclaimableThresholdBytes {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "info",
				Category:    "resource_exhaustion",
				Title:       "High Reclaimable Docker Storage",
				Message:     fmt.Sprintf("%.2f GB of reclaimable images/build cache/stopped containers detected on Docker host.", float64(diskUsage.TotalReclaimable)/(1024*1024*1024)),
				Remediation: "Run garbage collection or prune unused images/build cache to reclaim host disk space.",
			})
		}
	} else {
		report.Resources.DiskUsage = DiskUsageSummary{
			DockerAvailable: false,
			CollectedAt:     now,
			Warning:         firstNonEmptyDiag(dockerDiag.Error, "Docker daemon unavailable"),
		}
	}

	// 3. Deployment Failures & Service Runtime Resource Pressure
	e.collectProjectDiagnostics(ctx, projects, report)

	// 4. Repository Imports
	e.collectImportDiagnostics(ctx, scope, projectID, ownerID, projects, importSvc, report)

	// Sort issues by severity (critical -> warning -> info) and compute overall status
	severityRank := func(sev string) int {
		switch sev {
		case "critical":
			return 0
		case "warning":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(report.Issues, func(i, j int) bool {
		return severityRank(report.Issues[i].Severity) < severityRank(report.Issues[j].Severity)
	})
	for _, issue := range report.Issues {
		if issue.Severity == "critical" {
			report.OverallStatus = "critical"
			break
		}
		if issue.Severity == "warning" && report.OverallStatus == "healthy" {
			report.OverallStatus = "degraded"
		}
	}

	return report
}

func (e *Engine) collectDockerConnectivity(ctx context.Context) DockerConnectivityDiagnostics {
	diag := DockerConnectivityDiagnostics{
		Reconciliation: e.GetReconciliationState(),
	}
	if bVer, ok := e.getCachedBuilderVersion(); ok && bVer != "" {
		diag.BuilderVersion = string(bVer)
	}
	if e.dockerClient == nil {
		diag.Available = false
		diag.Error = "docker client is not initialized"
		return diag
	}

	diagCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	start := time.Now()
	ping, err := e.dockerClient.Ping(diagCtx, client.PingOptions{})
	diag.PingLatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		diag.Available = false
		diag.Error = fmt.Sprintf("docker ping failed: %v", err)
		return diag
	}

	diag.Available = true
	diag.APIVersion = ping.APIVersion
	if ping.BuilderVersion != "" {
		diag.BuilderVersion = string(ping.BuilderVersion)
		e.setCachedBuilderVersion(ping.BuilderVersion)
	}

	if ver, vErr := e.dockerClient.ServerVersion(diagCtx, client.ServerVersionOptions{}); vErr == nil {
		diag.ServerVersion = ver.Version
		if ver.Os != "" {
			diag.OSType = ver.Os
		}
		diag.Architecture = ver.Arch
	}

	if infoRes, iErr := e.dockerClient.Info(diagCtx, client.InfoOptions{}); iErr == nil {
		info := infoRes.Info
		diag.StorageDriver = info.Driver
		diag.OperatingSystem = info.OperatingSystem
		if info.OSType != "" {
			diag.OSType = info.OSType
		}
		diag.CPUs = info.NCPU
		diag.MemoryTotalBytes = info.MemTotal
		if len(info.Warnings) > 0 {
			diag.DaemonWarnings = append([]string(nil), info.Warnings...)
		}
	}

	return diag
}

func (e *Engine) collectProjectDiagnostics(ctx context.Context, projects []*models.Project, report *DiagnosticsReport) {
	if len(projects) == 0 {
		return
	}

	// Snapshot in-memory runtime health strike/crash counters once
	e.healthStrikeMu.Lock()
	strikesSnap := make(map[uuid.UUID]int, len(e.healthStrikes))
	for k, v := range e.healthStrikes {
		strikesSnap[k] = v
	}
	restartsSnap := make(map[uuid.UUID]int, len(e.selfHealRestarts))
	for k, v := range e.selfHealRestarts {
		restartsSnap[k] = v
	}
	crashCountSnap := make(map[uuid.UUID]int, len(e.crashEvents))
	cutoff := time.Now().Add(-e.crashLoopWindow)
	if e.crashLoopWindow <= 0 {
		cutoff = time.Now().Add(-10 * time.Minute)
	}
	for k, list := range e.crashEvents {
		cnt := 0
		for _, ts := range list {
			if !ts.Before(cutoff) {
				cnt++
			}
		}
		if cnt > 0 {
			crashCountSnap[k] = cnt
		}
	}
	e.healthStrikeMu.Unlock()

	var failures []DeploymentFailureDiagnostic

	for _, proj := range projects {
		if proj == nil {
			continue
		}

		// Collect release deployment failures
		if e.deploymentService != nil {
			if deps, err := e.deploymentService.ListDeployments(ctx, proj.ID, 10, 0); err == nil {
				for _, d := range deps {
					if d == nil {
						continue
					}
					if d.Status == models.DeployStatusFailed || d.Status == models.DeployStatusCrashed {
						reason := ""
						if d.FailureReason != nil {
							reason = *d.FailureReason
						}
						failures = append(failures, DeploymentFailureDiagnostic{
							Scope:         "release",
							ID:            d.ID,
							ProjectID:     proj.ID,
							ProjectName:   proj.Name,
							DeployNumber:  d.DeployNumber,
							Status:        d.Status,
							FailureReason: reason,
							CreatedAt:     d.CreatedAt,
							FinishedAt:    d.FinishedAt,
						})
					}
				}
			}
		}

		// Collect services for failure & resource checks
		var svcs []*models.Service
		if e.serviceService != nil {
			if list, err := e.serviceService.ListServices(ctx, proj.ID); err == nil && len(list) > 0 {
				svcs = list
			}
		}
		if len(svcs) == 0 && len(proj.Services) > 0 {
			svcs = proj.Services
		}

		if e.deploymentService != nil {
			for _, svc := range svcs {
				if svc == nil {
					continue
				}
				sds, err := e.deploymentService.ListServiceDeploymentsByService(ctx, svc.ID)
				if err != nil {
					continue
				}
				checked := 0
				for _, sd := range sds {
					if sd == nil {
						continue
					}
					checked++
					if checked > 5 {
						break
					}
					if sd.Status == models.DeployStatusFailed || sd.Status == models.DeployStatusCrashed {
						reason := ""
						if sd.FailureReason != nil {
							reason = *sd.FailureReason
						}
						sid := svc.ID
						failures = append(failures, DeploymentFailureDiagnostic{
							Scope:         "service",
							ID:            sd.ID,
							ProjectID:     proj.ID,
							ProjectName:   proj.Name,
							ServiceID:     &sid,
							ServiceName:   svc.Name,
							DeployNumber:  sd.DeployNumber,
							Status:        sd.Status,
							FailureReason: reason,
							CreatedAt:     sd.CreatedAt,
							FinishedAt:    sd.FinishedAt,
						})
					}
				}
			}
		}

		// Collect cached/live container metrics to detect resource exhaustion or health degradation
		if e.dockerClient != nil {
			metrics, err := e.GetProjectRuntimeMetricsWithOptions(ctx, proj.ID, false)
			if err == nil && metrics != nil {
				for _, m := range metrics.Services {
					selfHealCnt := restartsSnap[m.ServiceID]
					crashCnt := crashCountSnap[m.ServiceID]
					strikeCnt := strikesSnap[m.ServiceID]

					var reasons []string
					sev := ""
					if m.OOMKilled {
						reasons = append(reasons, "Container was terminated by kernel OOM killer")
						sev = "critical"
					}
					if m.MemoryPercent >= highMemoryWarningThresholdPct {
						reasons = append(reasons, fmt.Sprintf("Memory utilization is %.1f%% of configured limit", m.MemoryPercent))
						if sev == "" {
							sev = "warning"
						}
					}
					if m.HealthStatus == "unhealthy" || m.HealthFailingStreak > 0 || strikeCnt > 0 {
						reasons = append(reasons, fmt.Sprintf("Health probe %s (failing streak: %d, rollback strikes: %d)", firstNonEmptyDiag(m.HealthStatus, "degraded"), m.HealthFailingStreak, strikeCnt))
						if sev == "" {
							sev = "warning"
						}
					}
					if crashCnt > 0 || selfHealCnt > 0 {
						reasons = append(reasons, fmt.Sprintf("Recent crash/self-heal activity (%d crash event(s), %d self-heal restart(s))", crashCnt, selfHealCnt))
						if sev == "" {
							sev = "warning"
						}
					}

					if len(reasons) > 0 {
						joinedReason := strings.Join(reasons, "; ")
						report.Resources.ExhaustedServices = append(report.Resources.ExhaustedServices, ServiceResourceWarning{
							ProjectID:           proj.ID,
							ProjectName:         proj.Name,
							ServiceID:           m.ServiceID,
							ServiceName:         m.ServiceName,
							Status:              m.Status,
							CPUPercent:          m.CPUPercent,
							MemoryUsedBytes:     m.MemoryUsedBytes,
							MemoryLimitBytes:    m.MemoryLimitBytes,
							MemoryPercent:       m.MemoryPercent,
							OOMKilled:           m.OOMKilled,
							RestartCount:        m.RestartCount,
							HealthStatus:        m.HealthStatus,
							HealthFailingStreak: m.HealthFailingStreak,
							SelfHealRestarts:    selfHealCnt,
							CrashLoopEvents:     crashCnt,
							Reason:              joinedReason,
						})
						remediation := "Inspect service logs and runtime metrics; increase memory_mb quota or resolve failing health probes."
						if m.OOMKilled || m.MemoryPercent >= highMemoryWarningThresholdPct {
							remediation = "Increase the service memory_mb resource limit in Service Settings and redeploy."
						}
						report.Issues = append(report.Issues, DiagnosticIssue{
							Severity:    sev,
							Category:    "resource_exhaustion",
							Title:       fmt.Sprintf("Resource / Health Pressure on '%s'", m.ServiceName),
							Message:     joinedReason,
							Remediation: remediation,
							ResourceID:  m.ServiceID.String(),
						})
					}
				}
			}
		}
	}

	sort.SliceStable(failures, func(i, j int) bool {
		return failures[i].CreatedAt.After(failures[j].CreatedAt)
	})
	if len(failures) > 20 {
		failures = failures[:20]
	}
	report.DeploymentFailures = failures

	// Emit diagnostic issues for the most recent 5 deployment failures to keep issues concise and actionable
	for i, f := range failures {
		if i >= 5 {
			break
		}
		target := f.ProjectName
		if f.ServiceName != "" {
			target = fmt.Sprintf("%s / %s", f.ProjectName, f.ServiceName)
		}
		report.Issues = append(report.Issues, DiagnosticIssue{
			Severity:    "warning",
			Category:    "deployment_failure",
			Title:       fmt.Sprintf("%s #%d (%s)", strings.ToUpper(f.Scope), f.DeployNumber, target),
			Message:     firstNonEmptyDiag(f.FailureReason, fmt.Sprintf("Deployment ended with status '%s'", f.Status)),
			Remediation: "Open the deployment terminal logs to review build/health errors and retry or rollback once resolved.",
			ResourceID:  f.ID.String(),
		})
	}
}

func (e *Engine) collectImportDiagnostics(
	ctx context.Context,
	scope string,
	projectID *uuid.UUID,
	ownerID uuid.UUID,
	projects []*models.Project,
	importSvc *services.RepositoryImportService,
	report *DiagnosticsReport,
) {
	if importSvc == nil || ownerID == uuid.Nil {
		return
	}
	imports, err := importSvc.ListImports(ctx, ownerID, 20)
	if err != nil {
		return
	}

	var targetProject *models.Project
	if scope == "project" && projectID != nil && len(projects) > 0 {
		targetProject = projects[0]
	}

	now := time.Now().UTC()
	for _, imp := range imports {
		if imp == nil {
			continue
		}
		if targetProject != nil {
			matched := imp.ProjectID != nil && *imp.ProjectID == targetProject.ID
			if !matched && targetProject.SourceType == models.SourceTypeGitHub && strings.EqualFold(imp.RepositoryFullName, targetProject.SourceReference) {
				matched = true
			}
			if !matched {
				continue
			}
		}

		stale := models.IsImportInProgressStatus(imp.Status) && now.Sub(imp.UpdatedAt) > staleImportThreshold
		diag := ImportDiagnostic{
			ImportID:     imp.ID,
			Repository:   imp.RepositoryFullName,
			Branch:       imp.Branch,
			Status:       imp.Status,
			Stage:        imp.Stage,
			Progress:     imp.Progress,
			ErrorCode:    imp.ErrorCode,
			ErrorMessage: imp.ErrorMessage,
			Stale:        stale,
			ProjectID:    imp.ProjectID,
			UpdatedAt:    imp.UpdatedAt,
		}
		report.Imports = append(report.Imports, diag)

		if imp.Status == models.ImportStatusFailed {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "warning",
				Category:    "repository_import",
				Title:       fmt.Sprintf("Repository Import Failed: %s", imp.RepositoryFullName),
				Message:     firstNonEmptyDiag(imp.ErrorMessage, imp.Stage, "Repository import failed"),
				Remediation: "Verify GitHub repository permissions/branch name and trigger Retry Import from the dashboard.",
				ResourceID:  imp.ID.String(),
			})
		} else if stale {
			report.Issues = append(report.Issues, DiagnosticIssue{
				Severity:    "warning",
				Category:    "repository_import",
				Title:       fmt.Sprintf("Stalled Repository Import: %s", imp.RepositoryFullName),
				Message:     fmt.Sprintf("Import has remained in status '%s' since %s.", imp.Status, imp.UpdatedAt.Format(time.RFC3339)),
				Remediation: "Cancel and retry the import job, or restart the ForgeLAB backend to run automatic import recovery.",
				ResourceID:  imp.ID.String(),
			})
		}
	}
}

func firstNonEmptyDiag(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
