package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	DeploymentQueueKey       = "forgelab:queue:deployments"
	ProcessingQueueKey       = "forgelab:queue:deployments:processing"
	DeadLetterQueueKey       = "forgelab:queue:deployments:dead_letter"
	JobProcessingPrefix      = "forgelab:job:processing:"
	JobAttemptsPrefix        = "forgelab:job:attempts:"
	JobLeasePrefix           = "forgelab:job:lease:"
	JobStatePrefix           = "forgelab:job:state:"
	DefaultMaxRetries        = 3
	DefaultRetryBackoff      = 3 * time.Second
	DefaultWorkerCount       = 4
	DefaultLeaseTTL          = 60 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second

	JobStatePending    = "pending"
	JobStateProcessing = "processing"
	JobStateDLQ        = "dead_letter"
	JobStateCompleted  = "completed"
)

var (
	ErrJobAlreadyProcessing = errors.New("deployment job is already being processed")
	ErrInvalidJobPayload    = errors.New("invalid job payload: strictly typed JSON payload is required")
	ErrLeaseLost            = errors.New("job lease lost: expired or transferred to replacement worker")
)

type JobType string

const (
	JobTypeServiceDeployment JobType = "service_deployment"
	JobTypeDeployment        JobType = "deployment"
)

// Job represents a queued deployment unit with explicit typing.
type Job struct {
	Type                JobType    `json:"type"`
	ID                  uuid.UUID  `json:"id"`
	ServiceDeploymentID uuid.UUID  `json:"service_deployment_id,omitempty"`
	DeploymentID        *uuid.UUID `json:"deployment_id,omitempty"`
	ProjectID           *uuid.UUID `json:"project_id,omitempty"`
}

// Encode serializes the job to a JSON string.
func (j Job) Encode() string {
	b, _ := json.Marshal(j)
	return string(b)
}

// ParseJob decodes a queue item string into a strictly typed Job.
// Untyped or legacy plain UUID strings are rejected to eliminate ambiguity
// between service deployments and release deployments.
func ParseJob(item string) (Job, error) {
	trimmed := strings.TrimSpace(item)
	if !strings.HasPrefix(trimmed, "{") {
		return Job{}, fmt.Errorf("%w: untyped plain UUID representation is rejected to prevent release vs service deployment confusion", ErrInvalidJobPayload)
	}

	var j Job
	if err := json.Unmarshal([]byte(trimmed), &j); err != nil {
		return Job{}, fmt.Errorf("%w: unmarshal failed: %v", ErrInvalidJobPayload, err)
	}

	if j.Type != JobTypeServiceDeployment && j.Type != JobTypeDeployment {
		return Job{}, fmt.Errorf("%w: unknown or missing job type %q", ErrInvalidJobPayload, j.Type)
	}

	if j.ID == uuid.Nil {
		if j.ServiceDeploymentID != uuid.Nil {
			j.ID = j.ServiceDeploymentID
		} else if j.DeploymentID != nil && *j.DeploymentID != uuid.Nil {
			j.ID = *j.DeploymentID
		} else {
			return Job{}, fmt.Errorf("%w: missing deployment ID", ErrInvalidJobPayload)
		}
	}

	return j, nil
}

// TerminalChecker is an optional function that reports whether a job is already in a terminal state.
type TerminalChecker func(ctx context.Context, job Job) (bool, error)

type DeploymentQueue struct {
	client                *redis.Client
	maxRetries            int
	workerCount           int
	retryBackoff          time.Duration
	leaseTTL              time.Duration
	heartbeatInterval     time.Duration
	terminalChecker       TerminalChecker
	totalRecoveredJobs    int64
	totalRetryAttempts    int64
	totalDeadLetterJobs   int64
	totalTerminalCleanups int64
	wg                    sync.WaitGroup
	cancel                context.CancelFunc
}

func NewDeploymentQueue(client *redis.Client) *DeploymentQueue {
	return &DeploymentQueue{
		client:            client,
		maxRetries:        DefaultMaxRetries,
		workerCount:       DefaultWorkerCount,
		retryBackoff:      DefaultRetryBackoff,
		leaseTTL:          DefaultLeaseTTL,
		heartbeatInterval: DefaultHeartbeatInterval,
	}
}

// SetTerminalChecker configures a callback to check if a job is in a terminal state before retrying.
func (q *DeploymentQueue) SetTerminalChecker(tc TerminalChecker) {
	q.terminalChecker = tc
}

// SetMaxRetries configures the maximum retry attempts for a failing deployment job.
func (q *DeploymentQueue) SetMaxRetries(n int) {
	if n > 0 {
		q.maxRetries = n
	}
}

// SetWorkerCount sets the number of concurrent worker goroutines.
func (q *DeploymentQueue) SetWorkerCount(n int) {
	if n > 0 {
		q.workerCount = n
	}
}

// SetRetryBackoff sets the backoff duration before retrying failed jobs.
func (q *DeploymentQueue) SetRetryBackoff(d time.Duration) {
	if d > 0 {
		q.retryBackoff = d
	}
}

// SetLeaseTTL sets the expiration time for worker leases.
func (q *DeploymentQueue) SetLeaseTTL(ttl time.Duration) {
	if ttl > 0 {
		q.leaseTTL = ttl
	}
}

// SetHeartbeatInterval sets the heartbeat renewal frequency.
func (q *DeploymentQueue) SetHeartbeatInterval(interval time.Duration) {
	if interval > 0 {
		q.heartbeatInterval = interval
	}
}

// EnqueueServiceDeployment pushes a service deployment to the Redis queue using strictly typed payload.
func (q *DeploymentQueue) EnqueueServiceDeployment(ctx context.Context, serviceDeploymentID uuid.UUID) error {
	job := Job{
		Type:                JobTypeServiceDeployment,
		ID:                  serviceDeploymentID,
		ServiceDeploymentID: serviceDeploymentID,
	}
	payload := job.Encode()
	pipe := q.client.Pipeline()
	pipe.LPush(ctx, DeploymentQueueKey, payload)
	pipe.Set(ctx, JobStatePrefix+serviceDeploymentID.String(), JobStatePending, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to enqueue service deployment %s: %w", serviceDeploymentID, err)
	}
	slog.Info("enqueued service deployment job", "service_deployment_id", serviceDeploymentID)
	return nil
}

// EnqueueDeployment pushes a deployment / release ID to the Redis queue using strictly typed payload.
func (q *DeploymentQueue) EnqueueDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	job := Job{
		Type:         JobTypeDeployment,
		ID:           deploymentID,
		DeploymentID: &deploymentID,
	}
	payload := job.Encode()
	pipe := q.client.Pipeline()
	pipe.LPush(ctx, DeploymentQueueKey, payload)
	pipe.Set(ctx, JobStatePrefix+deploymentID.String(), JobStatePending, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to enqueue deployment %s: %w", deploymentID, err)
	}
	slog.Info("enqueued release deployment job", "deployment_id", deploymentID)
	return nil
}

// GetQueueStats returns the count of pending, in-flight, and dead-letter jobs.
func (q *DeploymentQueue) GetQueueStats(ctx context.Context) (pending, processing, deadLetter int64, err error) {
	pending, err = q.client.LLen(ctx, DeploymentQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	processing, err = q.client.LLen(ctx, ProcessingQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	deadLetter, err = q.client.LLen(ctx, DeadLetterQueueKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, 0, err
	}
	return pending, processing, deadLetter, nil
}

// ActiveLeaseInfo describes a currently running job lease held by a worker.
type ActiveLeaseInfo struct {
	JobID               string `json:"job_id"`
	JobType             string `json:"job_type,omitempty"`
	WorkerID            string `json:"worker_id,omitempty"`
	LeaseTTLRemainingMs int64  `json:"lease_ttl_remaining_ms"`
	Attempts            int    `json:"attempts"`
}

// DeadLetterEntry summarizes a job present in the dead-letter queue for diagnostics.
type DeadLetterEntry struct {
	JobID      string `json:"job_id,omitempty"`
	JobType    string `json:"job_type,omitempty"`
	Attempts   int    `json:"attempts"`
	RawPayload string `json:"raw_payload,omitempty"`
}

// QueueDiagnostics provides diagnostic metrics for queue monitoring and recovery observability.
type QueueDiagnostics struct {
	QueueDepth          int64             `json:"queue_depth"`
	PendingJobs         int64             `json:"pending_jobs"`
	ProcessingJobs      int64             `json:"processing_jobs"`
	DeadLetterJobs      int64             `json:"dead_letter_jobs"`
	TotalRecoveredJobs  int64             `json:"total_recovered_jobs"`
	TotalRetryAttempts  int64             `json:"total_retry_attempts"`
	TotalDeadLetterJobs int64             `json:"total_dead_letter_jobs"`
	MaxRetries          int               `json:"max_retries"`
	LeaseTTLSeconds     int64             `json:"lease_ttl_seconds"`
	RetryingJobs        int64             `json:"retrying_jobs"`
	ActiveLeases        []ActiveLeaseInfo `json:"active_leases,omitempty"`
	DeadLetterEntries   []DeadLetterEntry `json:"dead_letter_entries,omitempty"`
}

func extractWorkerIDFromLeaseToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if idx := strings.LastIndex(trimmed, ":"); idx > 0 {
		suffix := trimmed[idx+1:]
		if _, err := uuid.Parse(suffix); err == nil {
			return trimmed[:idx]
		}
	}
	return trimmed
}

func (q *DeploymentQueue) scanKeysBounded(ctx context.Context, pattern string, limit int) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, nextCursor, err := q.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return keys, err
		}
		keys = append(keys, batch...)
		if nextCursor == 0 || (limit > 0 && len(keys) >= limit) {
			break
		}
		cursor = nextCursor
	}
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

// GetQueueDiagnostics returns comprehensive queue counts, retry counters, active worker leases, and DLQ inspection.
func (q *DeploymentQueue) GetQueueDiagnostics(ctx context.Context) (QueueDiagnostics, error) {
	p, proc, dlq, err := q.GetQueueStats(ctx)
	if err != nil {
		return QueueDiagnostics{}, err
	}

	diag := QueueDiagnostics{
		QueueDepth:          p + proc,
		PendingJobs:         p,
		ProcessingJobs:      proc,
		DeadLetterJobs:      dlq,
		TotalRecoveredJobs:  atomic.LoadInt64(&q.totalRecoveredJobs),
		TotalRetryAttempts:  atomic.LoadInt64(&q.totalRetryAttempts),
		TotalDeadLetterJobs: atomic.LoadInt64(&q.totalDeadLetterJobs),
		MaxRetries:          q.maxRetries,
		LeaseTTLSeconds:     int64(q.leaseTTL.Seconds()),
	}

	// Index job types from processing queue so active leases include job_type
	jobTypeByID := make(map[string]string)
	if proc > 0 {
		if procItems, lErr := q.client.LRange(ctx, ProcessingQueueKey, 0, 99).Result(); lErr == nil {
			for _, item := range procItems {
				if job, pErr := ParseJob(item); pErr == nil {
					jobTypeByID[job.ID.String()] = string(job.Type)
				}
			}
		}
	}

	// Scan attempt counters to measure retrying jobs and annotate leases/DLQ entries
	attemptsByID := make(map[string]int)
	if attemptKeys, sErr := q.scanKeysBounded(ctx, JobAttemptsPrefix+"*", 500); sErr == nil {
		for _, k := range attemptKeys {
			jobID := strings.TrimPrefix(k, JobAttemptsPrefix)
			val, gErr := q.client.Get(ctx, k).Result()
			if gErr != nil {
				continue
			}
			n, cErr := strconv.Atoi(val)
			if cErr != nil {
				continue
			}
			attemptsByID[jobID] = n
			if n > 0 {
				diag.RetryingJobs++
			}
		}
	}

	// Inspect active leases and remaining TTLs
	if leaseKeys, sErr := q.scanKeysBounded(ctx, JobLeasePrefix+"*", 200); sErr == nil {
		for _, k := range leaseKeys {
			jobID := strings.TrimPrefix(k, JobLeasePrefix)
			token, gErr := q.client.Get(ctx, k).Result()
			if gErr != nil {
				continue
			}
			pttl, pErr := q.client.PTTL(ctx, k).Result()
			var ttlMs int64
			if pErr == nil && pttl > 0 {
				ttlMs = pttl.Milliseconds()
			}
			diag.ActiveLeases = append(diag.ActiveLeases, ActiveLeaseInfo{
				JobID:               jobID,
				JobType:             jobTypeByID[jobID],
				WorkerID:            extractWorkerIDFromLeaseToken(token),
				LeaseTTLRemainingMs: ttlMs,
				Attempts:            attemptsByID[jobID],
			})
		}
	}

	// Inspect up to 20 most recent dead-letter queue entries
	if dlq > 0 {
		if dlqItems, lErr := q.client.LRange(ctx, DeadLetterQueueKey, 0, 19).Result(); lErr == nil {
			for _, item := range dlqItems {
				entry := DeadLetterEntry{}
				if job, pErr := ParseJob(item); pErr == nil {
					idStr := job.ID.String()
					entry.JobID = idStr
					entry.JobType = string(job.Type)
					if att, ok := attemptsByID[idStr]; ok {
						entry.Attempts = att
					} else if att, aErr := q.GetAttempts(ctx, job.ID); aErr == nil {
						entry.Attempts = att
					}
				} else {
					entry.JobType = "corrupt"
					raw := item
					if len(raw) > 256 {
						raw = raw[:256] + "...[truncated]"
					}
					entry.RawPayload = raw
				}
				diag.DeadLetterEntries = append(diag.DeadLetterEntries, entry)
			}
		}
	}

	return diag, nil
}

// GetTotalRecoveredJobs returns the total count of abandoned jobs successfully recovered by this queue instance.
func (q *DeploymentQueue) GetTotalRecoveredJobs() int64 {
	return atomic.LoadInt64(&q.totalRecoveredJobs)
}

// GetTotalRetryAttempts returns the total count of retry attempts handled by this queue instance.
func (q *DeploymentQueue) GetTotalRetryAttempts() int64 {
	return atomic.LoadInt64(&q.totalRetryAttempts)
}

// GetTotalDeadLetterJobs returns the total count of jobs moved to DLQ by this queue instance.
func (q *DeploymentQueue) GetTotalDeadLetterJobs() int64 {
	return atomic.LoadInt64(&q.totalDeadLetterJobs)
}
