package models

import (
	"time"

	"github.com/google/uuid"
)

// ServiceMetricsSample represents a single historical point-in-time container metrics sample for a service.
type ServiceMetricsSample struct {
	ID               uuid.UUID `json:"id"`
	ProjectID        uuid.UUID `json:"project_id"`
	ServiceID        uuid.UUID `json:"service_id"`
	ServiceName      string    `json:"service_name"`
	ContainerID      string    `json:"container_id,omitempty"`
	Running          bool      `json:"running"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryUsedBytes  uint64    `json:"memory_used_bytes"`
	MemoryLimitBytes uint64    `json:"memory_limit_bytes"`
	MemoryPercent    float64   `json:"memory_percent"`
	NetworkRxBytes   uint64    `json:"network_rx_bytes"`
	NetworkTxBytes   uint64    `json:"network_tx_bytes"`
	PidsCurrent      uint64    `json:"pids_current"`
	RestartCount     int       `json:"restart_count"`
	OOMKilled        bool      `json:"oom_killed"`
	HealthStatus     string    `json:"health_status"`
	SampledAt        time.Time `json:"sampled_at"`
}

// ServiceMetricsHistorySummary holds roll-up statistics per service for the queried time range.
type ServiceMetricsHistorySummary struct {
	ServiceID       uuid.UUID  `json:"service_id"`
	ServiceName     string     `json:"service_name"`
	SampleCount     int        `json:"sample_count"`
	PeakCPUPercent  float64    `json:"peak_cpu_percent"`
	AvgCPUPercent   float64    `json:"avg_cpu_percent"`
	PeakMemoryBytes uint64     `json:"peak_memory_bytes"`
	MaxRestartCount int        `json:"max_restart_count"`
	OOMCount        int        `json:"oom_count"`
	LatestSampledAt *time.Time `json:"latest_sampled_at,omitempty"`
}

// ProjectMetricsHistoryResponse is returned by GET /api/projects/{id}/metrics/history.
type ProjectMetricsHistoryResponse struct {
	ProjectID             uuid.UUID                      `json:"project_id"`
	From                  time.Time                      `json:"from"`
	To                    time.Time                      `json:"to"`
	RetentionDays         int                            `json:"retention_days"`
	SampleIntervalSeconds int                            `json:"sample_interval_seconds"`
	TotalSamples          int                            `json:"total_samples"`
	Summaries             []ServiceMetricsHistorySummary `json:"summaries"`
	Samples               []ServiceMetricsSample         `json:"samples"`
}
