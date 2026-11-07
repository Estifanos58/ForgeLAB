package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	client             *redis.Client
	maxRetries         int
	workerCount        int
	retryBackoff       time.Duration
	leaseTTL           time.Duration
	heartbeatInterval  time.Duration
	terminalChecker    TerminalChecker
	totalRecoveredJobs int64
	wg                 sync.WaitGroup
	cancel             context.CancelFunc
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

// QueueDiagnostics provides diagnostic metrics for queue monitoring and recovery observability.
type QueueDiagnostics struct {
	PendingJobs        int64 `json:"pending_jobs"`
	ProcessingJobs     int64 `json:"processing_jobs"`
	DeadLetterJobs     int64 `json:"dead_letter_jobs"`
	TotalRecoveredJobs int64 `json:"total_recovered_jobs"`
}

// GetQueueDiagnostics returns comprehensive queue counts and recovery metrics.
func (q *DeploymentQueue) GetQueueDiagnostics(ctx context.Context) (QueueDiagnostics, error) {
	p, proc, dlq, err := q.GetQueueStats(ctx)
	if err != nil {
		return QueueDiagnostics{}, err
	}
	return QueueDiagnostics{
		PendingJobs:        p,
		ProcessingJobs:     proc,
		DeadLetterJobs:     dlq,
		TotalRecoveredJobs: atomic.LoadInt64(&q.totalRecoveredJobs),
	}, nil
}

// GetTotalRecoveredJobs returns the total count of abandoned jobs successfully recovered by this queue instance.
func (q *DeploymentQueue) GetTotalRecoveredJobs() int64 {
	return atomic.LoadInt64(&q.totalRecoveredJobs)
}
