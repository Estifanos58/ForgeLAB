package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
)

func TestMetricsService_RecordQuerySummariesAndRetention(t *testing.T) {
	ctx := context.Background()
	svc := NewMetricsService(nil, 7, 15*time.Second)
	assert.Equal(t, 7, svc.RetentionDays())

	projectID := uuid.New()
	otherProjectID := uuid.New()
	apiSvcID := uuid.New()
	dbSvcID := uuid.New()
	now := time.Now().UTC()

	err := svc.RecordSamples(ctx, []models.ServiceMetricsSample{
		{
			ProjectID:        projectID,
			ServiceID:        apiSvcID,
			ServiceName:      "api",
			Running:          true,
			CPUPercent:       10.0,
			MemoryUsedBytes:  100 * 1024 * 1024,
			MemoryLimitBytes: 512 * 1024 * 1024,
			MemoryPercent:    19.5,
			NetworkRxBytes:   1000,
			NetworkTxBytes:   2000,
			RestartCount:     0,
			SampledAt:        now.Add(-20 * time.Minute),
		},
		{
			ProjectID:        projectID,
			ServiceID:        apiSvcID,
			ServiceName:      "api",
			Running:          true,
			CPUPercent:       30.0,
			MemoryUsedBytes:  250 * 1024 * 1024,
			MemoryLimitBytes: 512 * 1024 * 1024,
			MemoryPercent:    48.8,
			NetworkRxBytes:   3000,
			NetworkTxBytes:   5000,
			RestartCount:     2,
			OOMKilled:        true,
			SampledAt:        now.Add(-10 * time.Minute),
		},
		{
			ProjectID:       projectID,
			ServiceID:       dbSvcID,
			ServiceName:     "postgres",
			Running:         true,
			CPUPercent:      5.0,
			MemoryUsedBytes: 128 * 1024 * 1024,
			RestartCount:    1,
			SampledAt:       now.Add(-5 * time.Minute),
		},
		{
			// Old sample beyond 7-day retention cutoff
			ProjectID:       projectID,
			ServiceID:       apiSvcID,
			ServiceName:     "api",
			Running:         true,
			CPUPercent:      99.0,
			MemoryUsedBytes: 400 * 1024 * 1024,
			SampledAt:       now.Add(-9 * 24 * time.Hour),
		},
		{
			// Other project must remain isolated
			ProjectID:   otherProjectID,
			ServiceID:   uuid.New(),
			ServiceName: "other",
			Running:     true,
			CPUPercent:  77.0,
			SampledAt:   now.Add(-2 * time.Minute),
		},
	})
	require.NoError(t, err)

	// Query last 1 hour returns chronological samples & accurate summaries
	resp, err := svc.QueryProjectHistory(ctx, projectID, MetricsHistoryQueryOptions{
		From:  now.Add(-1 * time.Hour),
		To:    now.Add(time.Minute),
		Limit: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, projectID, resp.ProjectID)
	assert.Equal(t, 7, resp.RetentionDays)
	assert.Equal(t, 15, resp.SampleIntervalSeconds)
	require.Equal(t, 3, resp.TotalSamples)
	assert.Equal(t, "api", resp.Samples[0].ServiceName)
	assert.True(t, resp.Samples[0].SampledAt.Before(resp.Samples[1].SampledAt), "samples must be chronological oldest->newest")
	assert.Equal(t, "postgres", resp.Samples[2].ServiceName)

	require.Len(t, resp.Summaries, 2)
	assert.Equal(t, "api", resp.Summaries[0].ServiceName)
	assert.Equal(t, 2, resp.Summaries[0].SampleCount)
	assert.InDelta(t, 30.0, resp.Summaries[0].PeakCPUPercent, 0.01)
	assert.InDelta(t, 20.0, resp.Summaries[0].AvgCPUPercent, 0.01)
	assert.Equal(t, uint64(250*1024*1024), resp.Summaries[0].PeakMemoryBytes)
	assert.Equal(t, 2, resp.Summaries[0].MaxRestartCount)
	assert.Equal(t, 1, resp.Summaries[0].OOMCount)

	// ServiceID filter narrows query and summary
	apiOnly, err := svc.QueryProjectHistory(ctx, projectID, MetricsHistoryQueryOptions{
		From:      now.Add(-1 * time.Hour),
		To:        now.Add(time.Minute),
		ServiceID: &apiSvcID,
	})
	require.NoError(t, err)
	require.Equal(t, 2, apiOnly.TotalSamples)
	require.Len(t, apiOnly.Summaries, 1)
	assert.Equal(t, apiSvcID, apiOnly.Summaries[0].ServiceID)

	// PruneOldSamples deletes only the 9-day-old sample
	pruned, err := svc.PruneOldSamples(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pruned)

	// Wide window no longer contains pruned sample
	afterPrune, err := svc.QueryProjectHistory(ctx, projectID, MetricsHistoryQueryOptions{
		From: now.Add(-30 * 24 * time.Hour),
		To:   now.Add(time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, 3, afterPrune.TotalSamples)
}
