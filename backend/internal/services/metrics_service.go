package services

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
)

const (
	defaultMetricsHistoryLimit = 240
	maxMetricsHistoryLimit     = 2000
	maxMemMetricsSamples       = 10000
)

// MetricsHistoryQueryOptions specifies filters for querying historical service metrics.
type MetricsHistoryQueryOptions struct {
	From      time.Time
	To        time.Time
	ServiceID *uuid.UUID
	Limit     int
}

// MetricsService stores and queries historical CPU, memory, network, and restart metrics samples.
type MetricsService struct {
	db                    *pgxpool.Pool
	retentionDays         int
	sampleIntervalSeconds int

	memMu      sync.RWMutex
	memSamples []models.ServiceMetricsSample
}

// NewMetricsService creates a MetricsService with configurable retention and sampling interval metadata.
func NewMetricsService(db *pgxpool.Pool, retentionDays int, sampleInterval time.Duration) *MetricsService {
	if retentionDays < 0 {
		retentionDays = 0
	}
	intervalSecs := int(sampleInterval.Seconds())
	if intervalSecs < 0 {
		intervalSecs = 0
	}
	return &MetricsService{
		db:                    db,
		retentionDays:         retentionDays,
		sampleIntervalSeconds: intervalSecs,
	}
}

// RetentionDays returns the configured sample retention period in days.
func (s *MetricsService) RetentionDays() int {
	return s.retentionDays
}

// RecordSamples persists a slice of point-in-time service metrics samples.
func (s *MetricsService) RecordSamples(ctx context.Context, samples []models.ServiceMetricsSample) error {
	if len(samples) == 0 {
		return nil
	}

	normalized := make([]models.ServiceMetricsSample, 0, len(samples))
	now := time.Now().UTC()
	for _, sm := range samples {
		if sm.ProjectID == uuid.Nil || sm.ServiceID == uuid.Nil {
			continue
		}
		if sm.ID == uuid.Nil {
			sm.ID = uuid.New()
		}
		if sm.SampledAt.IsZero() {
			sm.SampledAt = now
		} else {
			sm.SampledAt = sm.SampledAt.UTC()
		}
		if sm.HealthStatus == "" {
			sm.HealthStatus = "none"
		}
		normalized = append(normalized, sm)
	}
	if len(normalized) == 0 {
		return nil
	}

	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		s.memSamples = append(s.memSamples, normalized...)
		if len(s.memSamples) > maxMemMetricsSamples {
			trim := len(s.memSamples) - maxMemMetricsSamples
			s.memSamples = append([]models.ServiceMetricsSample(nil), s.memSamples[trim:]...)
		}
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin metrics sample insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertQuery = `
		INSERT INTO service_metrics_samples (
			id, project_id, service_id, service_name, container_id, running,
			cpu_percent, memory_used_bytes, memory_limit_bytes, memory_percent,
			network_rx_bytes, network_tx_bytes, pids_current, restart_count,
			oom_killed, health_status, sampled_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14,
			$15, $16, $17
		)`

	for _, sm := range normalized {
		if _, err := tx.Exec(ctx, insertQuery,
			sm.ID, sm.ProjectID, sm.ServiceID, sm.ServiceName, sm.ContainerID, sm.Running,
			sm.CPUPercent, clampUint64ToInt64(sm.MemoryUsedBytes), clampUint64ToInt64(sm.MemoryLimitBytes), sm.MemoryPercent,
			clampUint64ToInt64(sm.NetworkRxBytes), clampUint64ToInt64(sm.NetworkTxBytes), clampUint64ToInt64(sm.PidsCurrent), sm.RestartCount,
			sm.OOMKilled, sm.HealthStatus, sm.SampledAt,
		); err != nil {
			return fmt.Errorf("insert service metrics sample: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit metrics samples: %w", err)
	}
	return nil
}

// QueryProjectHistory fetches chronological metrics history and per-service summaries for a project.
func (s *MetricsService) QueryProjectHistory(
	ctx context.Context,
	projectID uuid.UUID,
	opts MetricsHistoryQueryOptions,
) (*models.ProjectMetricsHistoryResponse, error) {
	now := time.Now().UTC()
	to := opts.To.UTC()
	if to.IsZero() {
		to = now
	}
	from := opts.From.UTC()
	if from.IsZero() {
		from = to.Add(-1 * time.Hour)
	}
	if from.After(to) {
		from = to.Add(-1 * time.Hour)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultMetricsHistoryLimit
	}
	if limit > maxMetricsHistoryLimit {
		limit = maxMetricsHistoryLimit
	}

	var samples []models.ServiceMetricsSample
	if s.db == nil {
		s.memMu.RLock()
		for _, sm := range s.memSamples {
			if sm.ProjectID != projectID {
				continue
			}
			if opts.ServiceID != nil && *opts.ServiceID != uuid.Nil && sm.ServiceID != *opts.ServiceID {
				continue
			}
			if sm.SampledAt.Before(from) || sm.SampledAt.After(to) {
				continue
			}
			samples = append(samples, sm)
		}
		s.memMu.RUnlock()

		// Sort descending first to pick the newest N samples cleanly, then reverse to chronological ascending.
		sort.Slice(samples, func(i, j int) bool {
			return samples[i].SampledAt.After(samples[j].SampledAt)
		})
		if len(samples) > limit {
			samples = samples[:limit]
		}
	} else {
		rows, err := s.db.Query(ctx, `
			SELECT id, project_id, service_id, service_name, container_id, running,
			       cpu_percent, memory_used_bytes, memory_limit_bytes, memory_percent,
			       network_rx_bytes, network_tx_bytes, pids_current, restart_count,
			       oom_killed, health_status, sampled_at
			FROM service_metrics_samples
			WHERE project_id = $1
			  AND ($2::uuid IS NULL OR service_id = $2)
			  AND sampled_at >= $3
			  AND sampled_at <= $4
			ORDER BY sampled_at DESC
			LIMIT $5`,
			projectID, opts.ServiceID, from, to, limit,
		)
		if err != nil {
			return nil, fmt.Errorf("query project metrics history: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var sm models.ServiceMetricsSample
			var memUsed, memLimit, netRx, netTx, pids int64
			if err := rows.Scan(
				&sm.ID, &sm.ProjectID, &sm.ServiceID, &sm.ServiceName, &sm.ContainerID, &sm.Running,
				&sm.CPUPercent, &memUsed, &memLimit, &sm.MemoryPercent,
				&netRx, &netTx, &pids, &sm.RestartCount,
				&sm.OOMKilled, &sm.HealthStatus, &sm.SampledAt,
			); err != nil {
				return nil, fmt.Errorf("scan service metrics sample: %w", err)
			}
			sm.MemoryUsedBytes = safeInt64ToUint64(memUsed)
			sm.MemoryLimitBytes = safeInt64ToUint64(memLimit)
			sm.NetworkRxBytes = safeInt64ToUint64(netRx)
			sm.NetworkTxBytes = safeInt64ToUint64(netTx)
			sm.PidsCurrent = safeInt64ToUint64(pids)
			samples = append(samples, sm)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate project metrics history: %w", err)
		}
	}

	// Reverse into chronological order (oldest -> newest) for charts.
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	if samples == nil {
		samples = []models.ServiceMetricsSample{}
	}

	summaries := buildMetricsSummaries(samples)
	return &models.ProjectMetricsHistoryResponse{
		ProjectID:             projectID,
		From:                  from,
		To:                    to,
		RetentionDays:         s.retentionDays,
		SampleIntervalSeconds: s.sampleIntervalSeconds,
		TotalSamples:          len(samples),
		Summaries:             summaries,
		Samples:               samples,
	}, nil
}

// PruneOldSamples deletes metrics samples older than cutoffDays (0 disables pruning).
func (s *MetricsService) PruneOldSamples(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	return s.PruneSamplesBefore(ctx, cutoff)
}

// PruneSamplesBefore deletes metrics samples strictly older than cutoff.
func (s *MetricsService) PruneSamplesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		kept := s.memSamples[:0]
		var deleted int64
		for _, sm := range s.memSamples {
			if sm.SampledAt.Before(cutoff) {
				deleted++
				continue
			}
			kept = append(kept, sm)
		}
		s.memSamples = kept
		return deleted, nil
	}

	tag, err := s.db.Exec(ctx, `DELETE FROM service_metrics_samples WHERE sampled_at < $1`, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune expired metrics samples: %w", err)
	}
	return tag.RowsAffected(), nil
}

func buildMetricsSummaries(samples []models.ServiceMetricsSample) []models.ServiceMetricsHistorySummary {
	type acc struct {
		sumCPU  float64
		summary models.ServiceMetricsHistorySummary
	}
	bySvc := make(map[uuid.UUID]*acc)
	for i := range samples {
		sm := &samples[i]
		item, ok := bySvc[sm.ServiceID]
		if !ok {
			ts := sm.SampledAt
			bySvc[sm.ServiceID] = &acc{
				sumCPU: sm.CPUPercent,
				summary: models.ServiceMetricsHistorySummary{
					ServiceID:       sm.ServiceID,
					ServiceName:     sm.ServiceName,
					SampleCount:     1,
					PeakCPUPercent:  sm.CPUPercent,
					PeakMemoryBytes: sm.MemoryUsedBytes,
					MaxRestartCount: sm.RestartCount,
					OOMCount:        boolToInt(sm.OOMKilled),
					LatestSampledAt: &ts,
				},
			}
			continue
		}
		item.sumCPU += sm.CPUPercent
		item.summary.SampleCount++
		if sm.CPUPercent > item.summary.PeakCPUPercent {
			item.summary.PeakCPUPercent = sm.CPUPercent
		}
		if sm.MemoryUsedBytes > item.summary.PeakMemoryBytes {
			item.summary.PeakMemoryBytes = sm.MemoryUsedBytes
		}
		if sm.RestartCount > item.summary.MaxRestartCount {
			item.summary.MaxRestartCount = sm.RestartCount
		}
		if sm.OOMKilled {
			item.summary.OOMCount++
		}
		if item.summary.LatestSampledAt == nil || sm.SampledAt.After(*item.summary.LatestSampledAt) {
			ts := sm.SampledAt
			item.summary.LatestSampledAt = &ts
		}
	}

	out := make([]models.ServiceMetricsHistorySummary, 0, len(bySvc))
	for _, item := range bySvc {
		if item.summary.SampleCount > 0 {
			avg := item.sumCPU / float64(item.summary.SampleCount)
			item.summary.AvgCPUPercent = math.Round(avg*100) / 100
		}
		out = append(out, item.summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ServiceName < out[j].ServiceName
	})
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func clampUint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func safeInt64ToUint64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}
