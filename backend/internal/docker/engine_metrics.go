package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/forgelab/backend/internal/models"
)

// ServiceRuntimeMetrics holds live container telemetry and health details for a single service.
type ServiceRuntimeMetrics struct {
	ServiceID           uuid.UUID  `json:"service_id"`
	ServiceName         string     `json:"service_name"`
	Role                string     `json:"role"`
	Classification      string     `json:"classification"`
	Status              string     `json:"status"`
	ContainerID         *string    `json:"container_id,omitempty"`
	Running             bool       `json:"running"`
	RestartCount        int        `json:"restart_count"`
	OOMKilled           bool       `json:"oom_killed"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	UptimeSeconds       int64      `json:"uptime_seconds"`
	HealthStatus        string     `json:"health_status"` // healthy, unhealthy, starting, none
	HealthFailingStreak int        `json:"health_failing_streak"`
	LastHealthOutput    string     `json:"last_health_output,omitempty"`
	CPUPercent          float64    `json:"cpu_percent"`
	MemoryUsedBytes     uint64     `json:"memory_used_bytes"`
	MemoryLimitBytes    uint64     `json:"memory_limit_bytes"`
	MemoryPercent       float64    `json:"memory_percent"`
	CPULimitMillicores  int        `json:"cpu_limit_millicores"`
	MemoryLimitMB       int        `json:"memory_limit_mb"`
	NetworkRxBytes      uint64     `json:"network_rx_bytes"`
	NetworkTxBytes      uint64     `json:"network_tx_bytes"`
	PidsCurrent         uint64     `json:"pids_current"`
	CollectedAt         time.Time  `json:"collected_at"`
	MetricError         string     `json:"metric_error,omitempty"`
}

// ProjectRuntimeMetrics holds aggregated runtime telemetry across all services in a project.
type ProjectRuntimeMetrics struct {
	ProjectID            uuid.UUID               `json:"project_id"`
	ProjectName          string                  `json:"project_name"`
	Status               string                  `json:"status"`
	CollectedAt          time.Time               `json:"collected_at"`
	DockerAvailable      bool                    `json:"docker_available"`
	TotalCPUPercent      float64                 `json:"total_cpu_percent"`
	TotalMemoryUsedBytes uint64                  `json:"total_memory_used_bytes"`
	TotalNetworkRxBytes  uint64                  `json:"total_network_rx_bytes"`
	TotalNetworkTxBytes  uint64                  `json:"total_network_tx_bytes"`
	Services             []ServiceRuntimeMetrics `json:"services"`
}

// GetProjectRuntimeMetrics collects live CPU, memory, network, PID, uptime, and Docker health metrics
// for all services in the specified project.
func (e *Engine) GetProjectRuntimeMetrics(ctx context.Context, projectID uuid.UUID) (*ProjectRuntimeMetrics, error) {
	if e.projectService == nil {
		return nil, fmt.Errorf("project service is not initialized")
	}

	project, err := e.getProjectByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project %s: %w", projectID, err)
	}

	var svcs []*models.Service
	if e.serviceService != nil {
		svcs, err = e.serviceService.ListServices(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("failed to list services for project %s: %w", projectID, err)
		}
	}
	if len(svcs) == 0 && len(project.Services) > 0 {
		svcs = project.Services
	}

	now := time.Now().UTC()
	res := &ProjectRuntimeMetrics{
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		Status:          project.Status,
		CollectedAt:     now,
		DockerAvailable: e.dockerClient != nil,
		Services:        make([]ServiceRuntimeMetrics, len(svcs)),
	}

	if len(svcs) == 0 {
		return res, nil
	}

	// Collect metrics concurrently across services with a bounded worker pool
	workerLimit := 4
	sem := make(chan struct{}, workerLimit)
	var wg sync.WaitGroup

	for idx, svc := range svcs {
		wg.Add(1)
		go func(i int, s *models.Service) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res.Services[i] = e.collectServiceMetrics(ctx, s, now)
		}(idx, svc)
	}
	wg.Wait()

	for _, sm := range res.Services {
		res.TotalCPUPercent = round2Decimals(res.TotalCPUPercent + sm.CPUPercent)
		res.TotalMemoryUsedBytes += sm.MemoryUsedBytes
		res.TotalNetworkRxBytes += sm.NetworkRxBytes
		res.TotalNetworkTxBytes += sm.NetworkTxBytes
	}

	return res, nil
}

func (e *Engine) collectServiceMetrics(ctx context.Context, svc *models.Service, now time.Time) ServiceRuntimeMetrics {
	m := ServiceRuntimeMetrics{
		ServiceID:          svc.ID,
		ServiceName:        svc.Name,
		Role:               svc.Role,
		Classification:     svc.Classification,
		Status:             svc.Status,
		ContainerID:        svc.ContainerID,
		HealthStatus:       "none",
		CPULimitMillicores: svc.CpuMillicores,
		MemoryLimitMB:      svc.MemoryMB,
		CollectedAt:        now,
	}

	if svc.ContainerID == nil || strings.TrimSpace(*svc.ContainerID) == "" {
		return m
	}

	if e.dockerClient == nil {
		m.MetricError = "docker client unavailable"
		return m
	}

	cid := strings.TrimSpace(*svc.ContainerID)
	metricCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	inspRes, err := e.dockerClient.ContainerInspect(metricCtx, cid, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			m.MetricError = "container not found"
		} else {
			m.MetricError = fmt.Sprintf("container inspect failed: %v", err)
		}
		return m
	}

	insp := inspRes.Container
	m.RestartCount = insp.RestartCount
	if insp.State != nil {
		m.Running = insp.State.Running
		m.OOMKilled = insp.State.OOMKilled
		if insp.State.StartedAt != "" {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, insp.State.StartedAt); parseErr == nil && !parsed.IsZero() && parsed.Year() > 1970 {
				utcStarted := parsed.UTC()
				m.StartedAt = &utcStarted
				if m.Running {
					uptime := int64(now.Sub(utcStarted).Seconds())
					if uptime < 0 {
						uptime = 0
					}
					m.UptimeSeconds = uptime
				}
			}
		}
		if insp.State.Health != nil {
			if st := string(insp.State.Health.Status); st != "" {
				m.HealthStatus = st
			}
			m.HealthFailingStreak = insp.State.Health.FailingStreak
			m.LastHealthOutput = extractLastHealthOutput(insp.State.Health)
		}
	}

	if !m.Running {
		return m
	}

	statsRes, err := e.dockerClient.ContainerStats(metricCtx, cid, client.ContainerStatsOptions{
		Stream:                false,
		IncludePreviousSample: true,
	})
	if err != nil {
		m.MetricError = fmt.Sprintf("container stats failed: %v", err)
		return m
	}
	defer statsRes.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(statsRes.Body).Decode(&stats); err != nil {
		m.MetricError = fmt.Sprintf("failed to decode container stats: %v", err)
		return m
	}

	m.CPUPercent = calculateCPUPercent(&stats)
	usedBytes, limitBytes, memPct := calculateMemoryMetrics(&stats, svc.MemoryMB)
	m.MemoryUsedBytes = usedBytes
	m.MemoryLimitBytes = limitBytes
	m.MemoryPercent = memPct
	m.PidsCurrent = stats.PidsStats.Current

	for _, netStats := range stats.Networks {
		m.NetworkRxBytes += netStats.RxBytes
		m.NetworkTxBytes += netStats.TxBytes
	}

	return m
}

func calculateCPUPercent(stats *container.StatsResponse) float64 {
	if stats == nil {
		return 0
	}

	// Windows containers report processor count and 100ns ticks
	if strings.EqualFold(stats.OSType, "windows") {
		possIntervals := uint64(stats.Read.Sub(stats.PreRead).Nanoseconds())
		if possIntervals > 0 && stats.NumProcs > 0 && stats.CPUStats.CPUUsage.TotalUsage >= stats.PreCPUStats.CPUUsage.TotalUsage {
			possIntervals = (possIntervals / 100) * uint64(stats.NumProcs)
			intervalsUsed := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
			if possIntervals > 0 {
				return round2Decimals((float64(intervalsUsed) / float64(possIntervals)) * 100.0)
			}
		}
		return 0
	}

	if stats.CPUStats.CPUUsage.TotalUsage <= stats.PreCPUStats.CPUUsage.TotalUsage {
		return 0
	}
	if stats.CPUStats.SystemUsage <= stats.PreCPUStats.SystemUsage {
		return 0
	}

	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)

	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1.0
	}

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		return round2Decimals((cpuDelta / systemDelta) * onlineCPUs * 100.0)
	}
	return 0
}

func calculateMemoryMetrics(stats *container.StatsResponse, configuredLimitMB int) (used uint64, limit uint64, pct float64) {
	if stats == nil {
		if configuredLimitMB > 0 {
			return 0, uint64(configuredLimitMB) * 1024 * 1024, 0
		}
		return 0, 0, 0
	}

	if strings.EqualFold(stats.OSType, "windows") && stats.MemoryStats.Usage == 0 {
		used = stats.MemoryStats.PrivateWorkingSet
	} else {
		var cache uint64
		if stats.MemoryStats.Stats != nil {
			// cgroup v2 uses inactive_file; cgroup v1 uses total_inactive_file or cache
			if v, ok := stats.MemoryStats.Stats["inactive_file"]; ok && v > 0 {
				cache = v
			} else if v, ok := stats.MemoryStats.Stats["total_inactive_file"]; ok && v > 0 {
				cache = v
			} else if v, ok := stats.MemoryStats.Stats["cache"]; ok && v > 0 {
				cache = v
			}
		}
		if stats.MemoryStats.Usage > cache {
			used = stats.MemoryStats.Usage - cache
		} else {
			used = stats.MemoryStats.Usage
		}
	}

	limit = stats.MemoryStats.Limit
	if limit == 0 && configuredLimitMB > 0 {
		limit = uint64(configuredLimitMB) * 1024 * 1024
	}

	if limit > 0 {
		pct = round2Decimals((float64(used) / float64(limit)) * 100.0)
	}
	return used, limit, pct
}

func round2Decimals(val float64) float64 {
	return math.Round(val*100) / 100
}
