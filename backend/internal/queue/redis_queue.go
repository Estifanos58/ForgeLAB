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

// Lua scripts for ownership-safe lease operations and atomic recovery
var (
	// extendLeaseScript renews the lease TTL only if the caller still holds the matching token.
	extendLeaseScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("PEXPIRE", KEYS[1], ARGV[2])
		else
			return 0
		end
	`)

	// releaseLeaseScript deletes the lease only if the caller still holds the matching token.
	releaseLeaseScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		else
			return 0
		end
	`)

	// finalizeSuccessScript atomically removes the job from processing queue, deletes the lease,
	// deletes attempts, and clears state ONLY IF the caller holds the matching lease token.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: attemptsKey, KEYS[4]: stateKey
	// ARGV[1]: leaseToken, ARGV[2]: itemStr
	finalizeSuccessScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			redis.call("DEL", KEYS[1])
			redis.call("DEL", KEYS[3])
			redis.call("DEL", KEYS[4])
			return 1
		else
			return 0
		end
	`)

	// failAndRequeueScript atomically removes the job from processing queue, deletes the lease,
	// increments attempts, sets state to pending, and pushes to deployment queue
	// ONLY IF the caller holds the matching lease token.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: deploymentQueueKey, KEYS[4]: attemptsKey, KEYS[5]: stateKey
	// ARGV[1]: leaseToken, ARGV[2]: itemStr, ARGV[3]: statePending ("pending")
	failAndRequeueScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			redis.call("DEL", KEYS[1])
			local att = redis.call("INCR", KEYS[4])
			redis.call("EXPIRE", KEYS[4], 86400)
			redis.call("SET", KEYS[5], ARGV[3], "EX", 86400)
			redis.call("LPUSH", KEYS[3], ARGV[2])
			return att
		else
			return -1
		end
	`)

	// failToDLQScript atomically removes the job from processing queue, deletes the lease,
	// increments attempts, sets state to dead_letter, and pushes to DLQ
	// ONLY IF the caller holds the matching lease token.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: dlqKey, KEYS[4]: attemptsKey, KEYS[5]: stateKey
	// ARGV[1]: leaseToken, ARGV[2]: itemStr, ARGV[3]: stateDLQ ("dead_letter")
	failToDLQScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			redis.call("DEL", KEYS[1])
			local att = redis.call("INCR", KEYS[4])
			redis.call("EXPIRE", KEYS[4], 86400)
			redis.call("SET", KEYS[5], ARGV[3], "EX", 86400)
			redis.call("LPUSH", KEYS[3], ARGV[2])
			return att
		else
			return -1
		end
	`)

	// terminalCleanupScript atomically removes the job from processing queue, deletes the lease,
	// and clears state ONLY IF the caller holds the matching lease token.
	// If KEYS[4] (dlqKey) is provided, it pushes itemStr to DLQ.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: stateKey, [KEYS[4]: dlqKey]
	// ARGV[1]: leaseToken, ARGV[2]: itemStr
	terminalCleanupScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			redis.call("DEL", KEYS[1])
			redis.call("DEL", KEYS[3])
			if #KEYS >= 4 and KEYS[4] ~= "" then
				redis.call("LPUSH", KEYS[4], ARGV[2])
			end
			return 1
		else
			return 0
		end
	`)

	// lremProcessingWithLeaseScript atomically removes the item from processing queue
	// ONLY IF the caller holds the matching lease token.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey
	// ARGV[1]: leaseToken, ARGV[2]: itemStr
	lremProcessingWithLeaseScript = redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("LREM", KEYS[2], 1, ARGV[2])
		else
			return -1
		end
	`)

	// atomicRecoverScript atomically moves an abandoned job from processing queue to target queue (pending or DLQ)
	// or performs terminal cleanup only if its lease key does NOT exist (i.e. expired or orphaned)
	// and the job is actually present in the processing queue.
	// KEYS[1]: processingQueueKey, KEYS[2]: deploymentQueueKey, KEYS[3]: leaseKey, KEYS[4]: attemptsKey, KEYS[5]: stateKey, KEYS[6]: dlqKey
	// ARGV[1]: itemStr, ARGV[2]: mode ("terminal" or "retry"), ARGV[3]: maxRetries
	// Returns:
	//   0: lease still active OR item not found in processing queue (concurrency/duplicate safe)
	//   1: re-enqueued to pending queue
	//   2: moved to DLQ (attempts >= maxRetries)
	//   3: terminal cleanup (removed from processing, state deleted)
	atomicRecoverScript = redis.NewScript(`
		if redis.call("EXISTS", KEYS[3]) == 1 then
			return 0
		end
		local rem = redis.call("LREM", KEYS[1], 1, ARGV[1])
		if rem > 0 then
			if ARGV[2] == "terminal" then
				redis.call("DEL", KEYS[5])
				return 3
			end
			local att = redis.call("INCR", KEYS[4])
			redis.call("EXPIRE", KEYS[4], 86400)
			local maxRetries = tonumber(ARGV[3])
			if att >= maxRetries then
				redis.call("LPUSH", KEYS[6], ARGV[1])
				redis.call("SET", KEYS[5], "dead_letter", "EX", 86400)
				return 2
			else
				redis.call("LPUSH", KEYS[2], ARGV[1])
				redis.call("SET", KEYS[5], "pending", "EX", 86400)
				return 1
			end
		end
		return 0
	`)

	// acquireLeaseScript atomically acquires a job lease (SET NX PX) and sets the job state to
	// "processing" in a single atomic operation. This prevents the race where the worker crashes
	// between acquiring the lease and setting the state.
	// KEYS[1]: leaseKey, KEYS[2]: stateKey
	// ARGV[1]: token, ARGV[2]: leaseTTL (ms), ARGV[3]: stateProcessing ("processing"), ARGV[4]: stateTTL (seconds)
	// Returns 1 if acquired, 0 if already held.
	acquireLeaseScript = redis.NewScript(`
		local ok = redis.call("SET", KEYS[1], ARGV[1], "NX", "PX", ARGV[2])
		if ok then
			redis.call("SET", KEYS[2], ARGV[3], "EX", ARGV[4])
			return 1
		else
			return 0
		end
	`)

	// corruptToDLQScript atomically removes a corrupt/untyped payload from the processing queue
	// and pushes it to the dead-letter queue. This prevents the race where two concurrent recovery
	// sweeps or workers could both remove the same item.
	// KEYS[1]: processingQueueKey, KEYS[2]: dlqKey
	// ARGV[1]: itemStr
	// Returns the number of items removed (0 or 1).
	corruptToDLQScript = redis.NewScript(`
		local rem = redis.call("LREM", KEYS[1], 1, ARGV[1])
		if rem > 0 then
			redis.call("LPUSH", KEYS[2], ARGV[1])
		end
		return rem
	`)
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
	client            *redis.Client
	maxRetries        int
	workerCount       int
	retryBackoff      time.Duration
	leaseTTL          time.Duration
	heartbeatInterval time.Duration
	terminalChecker   TerminalChecker
	wg                sync.WaitGroup
	cancel            context.CancelFunc
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

// acquireLease atomically acquires an exclusive execution lease for a job AND sets its
// state to "processing" in a single Lua script. This prevents the race where a worker
// crashes between acquiring the lease and setting the state index.
// Returns a unique execution lease token if acquired.
func (q *DeploymentQueue) acquireLease(ctx context.Context, jobID uuid.UUID, workerID string) (string, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID
	token := fmt.Sprintf("%s:%s", workerID, uuid.New().String())

	res, err := acquireLeaseScript.Run(ctx, q.client,
		[]string{leaseKey, stateKey},
		token, q.leaseTTL.Milliseconds(), JobStateProcessing, int64(24*time.Hour/time.Second),
	).Result()
	if err != nil {
		return "", false, fmt.Errorf("failed to execute acquire lease script: %w", err)
	}
	acquired, ok := res.(int64)
	if !ok || acquired == 0 {
		return "", false, nil
	}

	return token, true, nil
}

// extendLease renews the TTL of the worker's lease ONLY if caller still owns the matching token.
func (q *DeploymentQueue) extendLease(ctx context.Context, jobID uuid.UUID, leaseToken string) error {
	leaseKey := JobLeasePrefix + jobID.String()
	res, err := extendLeaseScript.Run(ctx, q.client, []string{leaseKey}, leaseToken, q.leaseTTL.Milliseconds()).Result()
	if err != nil {
		return fmt.Errorf("failed to execute lease extension script: %w", err)
	}

	renewed, ok := res.(int64)
	if !ok || renewed == 0 {
		return ErrLeaseLost
	}
	return nil
}

// releaseLease deletes the lease ONLY if caller still owns the matching token.
func (q *DeploymentQueue) releaseLease(ctx context.Context, jobID uuid.UUID, leaseToken string) {
	leaseKey := JobLeasePrefix + jobID.String()
	_, _ = releaseLeaseScript.Run(ctx, q.client, []string{leaseKey}, leaseToken).Result()
}

// FinalizeSuccess atomically removes the job from the processing queue, deletes the lease,
// deletes attempts, and clears the state index only if caller holds the matching lease token.
func (q *DeploymentQueue) FinalizeSuccess(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := finalizeSuccessScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return false, fmt.Errorf("failed to execute finalize success script: %w", err)
	}
	val, ok := res.(int64)
	return ok && val == 1, nil
}

// FailAndRequeue atomically removes the job from processing queue, deletes the lease,
// increments attempts, sets state to pending, and re-enqueues into the deployment queue
// only if caller holds the matching lease token. Returns (newAttempts, success, error).
func (q *DeploymentQueue) FailAndRequeue(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := failAndRequeueScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeploymentQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr, JobStatePending,
	).Result()
	if err != nil {
		return 0, false, fmt.Errorf("failed to execute fail and requeue script: %w", err)
	}
	attempts, ok := res.(int64)
	if !ok || attempts < 0 {
		return 0, false, nil
	}
	return attempts, true, nil
}

// FailToDLQ atomically removes the job from processing queue, deletes the lease,
// increments attempts, sets state to dead_letter, and transfers to dead-letter queue
// only if caller holds the matching lease token. Returns (newAttempts, success, error).
func (q *DeploymentQueue) FailToDLQ(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	res, err := failToDLQScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey, DeadLetterQueueKey, attemptsKey, stateKey},
		leaseToken, itemStr, JobStateDLQ,
	).Result()
	if err != nil {
		return 0, false, fmt.Errorf("failed to execute fail to DLQ script: %w", err)
	}
	attempts, ok := res.(int64)
	if !ok || attempts < 0 {
		return 0, false, nil
	}
	return attempts, true, nil
}

// TerminalCleanup atomically removes the job from processing queue, deletes the lease,
// and deletes state only if caller holds the matching lease token. If moveToDLQ is true, pushes to DLQ.
func (q *DeploymentQueue) TerminalCleanup(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string, moveToDLQ bool) (bool, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	stateKey := JobStatePrefix + keyID

	keys := []string{leaseKey, ProcessingQueueKey, stateKey}
	if moveToDLQ {
		keys = append(keys, DeadLetterQueueKey)
	}

	res, err := terminalCleanupScript.Run(ctx, q.client,
		keys,
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return false, fmt.Errorf("failed to execute terminal cleanup script: %w", err)
	}
	val, ok := res.(int64)
	return ok && val == 1, nil
}

// LRemProcessingWithLease atomically removes item from processing queue only if leaseToken matches.
func (q *DeploymentQueue) LRemProcessingWithLease(ctx context.Context, jobID uuid.UUID, leaseToken, itemStr string) (int64, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	res, err := lremProcessingWithLeaseScript.Run(ctx, q.client,
		[]string{leaseKey, ProcessingQueueKey},
		leaseToken, itemStr,
	).Result()
	if err != nil {
		return 0, err
	}
	rem, ok := res.(int64)
	if !ok || rem < 0 {
		return 0, ErrLeaseLost
	}
	return rem, nil
}

// RecoverJobAtomic executes the atomic recovery script for an abandoned or orphaned job.
// Returns 0: not recovered (lease exists or not in processing queue), 1: re-enqueued, 2: DLQ, 3: terminal cleanup.
func (q *DeploymentQueue) RecoverJobAtomic(ctx context.Context, itemStr string, jobID uuid.UUID, maxRetries int, isTerminal bool) (int, error) {
	keyID := jobID.String()
	leaseKey := JobLeasePrefix + keyID
	attemptsKey := JobAttemptsPrefix + keyID
	stateKey := JobStatePrefix + keyID

	mode := "retry"
	if isTerminal {
		mode = "terminal"
	}

	res, err := atomicRecoverScript.Run(ctx, q.client,
		[]string{ProcessingQueueKey, DeploymentQueueKey, leaseKey, attemptsKey, stateKey, DeadLetterQueueKey},
		itemStr, mode, strconv.Itoa(maxRetries),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to execute atomic recovery script: %w", err)
	}
	code, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected return from atomic recovery script: %v", res)
	}
	return int(code), nil
}

// IsLeaseOwner checks if the given leaseToken currently owns the job lease.
func (q *DeploymentQueue) IsLeaseOwner(ctx context.Context, jobID uuid.UUID, leaseToken string) (bool, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	val, err := q.client.Get(ctx, leaseKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	return val == leaseToken, nil
}

// HasActiveLease returns true if an active, non-expired worker lease exists for the job.
func (q *DeploymentQueue) HasActiveLease(ctx context.Context, jobID uuid.UUID) (bool, error) {
	leaseKey := JobLeasePrefix + jobID.String()
	n, err := q.client.Exists(ctx, leaseKey).Result()
	return n > 0, err
}

// IsJobInQueue checks if the job is waiting in the pending queue.
// Checks explicit O(1) state key first, then falls back to list scan if not indexed.
func (q *DeploymentQueue) IsJobInQueue(ctx context.Context, jobID uuid.UUID) (bool, error) {
	length, err := q.client.LLen(ctx, DeploymentQueueKey).Result()
	if err != nil || length == 0 {
		return false, err
	}

	state, err := q.client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err == nil && state == JobStatePending {
		return true, nil
	}

	// Fallback to scanning queue list (e.g. for items pushed directly in tests)
	items, err := q.client.LRange(ctx, DeploymentQueueKey, 0, -1).Result()
	if err != nil {
		return false, err
	}
	idStr := jobID.String()
	for _, item := range items {
		if job, err := ParseJob(item); err == nil && job.ID.String() == idStr {
			return true, nil
		}
	}
	return false, nil
}

// IsJobInProcessing checks if the job is recorded in the processing queue or active lease.
// Checks explicit O(1) state key first, then falls back to list scan if not indexed.
func (q *DeploymentQueue) IsJobInProcessing(ctx context.Context, jobID uuid.UUID) (bool, error) {
	state, err := q.client.Get(ctx, JobStatePrefix+jobID.String()).Result()
	if err == nil && state == JobStateProcessing {
		return true, nil
	}

	length, err := q.client.LLen(ctx, ProcessingQueueKey).Result()
	if err != nil || length == 0 {
		return false, err
	}

	// Fallback to scanning processing list
	items, err := q.client.LRange(ctx, ProcessingQueueKey, 0, -1).Result()
	if err != nil {
		return false, err
	}
	idStr := jobID.String()
	for _, item := range items {
		if job, err := ParseJob(item); err == nil && job.ID.String() == idStr {
			return true, nil
		}
	}
	return false, nil
}

// IsJobEnqueuedOrActive returns true if the job is either in pending queue, processing queue, or has an active lease.
func (q *DeploymentQueue) IsJobEnqueuedOrActive(ctx context.Context, jobID uuid.UUID) (bool, error) {
	active, err := q.HasActiveLease(ctx, jobID)
	if err == nil && active {
		return true, nil
	}

	inQueue, err := q.IsJobInQueue(ctx, jobID)
	if err == nil && inQueue {
		return true, nil
	}
	inProc, err := q.IsJobInProcessing(ctx, jobID)
	if err == nil && inProc {
		return true, nil
	}
	return false, nil
}

// GetAttempts returns the number of execution attempts for a given job.
func (q *DeploymentQueue) GetAttempts(ctx context.Context, jobID uuid.UUID) (int, error) {
	attemptsStr, err := q.client.Get(ctx, JobAttemptsPrefix+jobID.String()).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, err
	}
	return strconv.Atoi(attemptsStr)
}

// IncrementAttempts increments the retry attempt counter in Redis.
func (q *DeploymentQueue) IncrementAttempts(ctx context.Context, jobID uuid.UUID) (int64, error) {
	key := JobAttemptsPrefix + jobID.String()
	n, err := q.client.Incr(ctx, key).Result()
	if err == nil {
		q.client.Expire(ctx, key, 24*time.Hour)
	}
	return n, err
}

// ResetAttempts clears the attempt counter for a job.
func (q *DeploymentQueue) ResetAttempts(ctx context.Context, jobID uuid.UUID) error {
	return q.client.Del(ctx, JobAttemptsPrefix+jobID.String()).Err()
}

// RecoverAbandonedJobs inspects jobs remaining in the processing queue.
// If a job's lease has expired (e.g. worker process crashed), it checks whether
// the deployment is already terminal. If not terminal and attempts < maxRetries,
// it atomically re-enqueues the job instead of waiting and failing.
func (q *DeploymentQueue) RecoverAbandonedJobs(ctx context.Context) (int, error) {
	items, err := q.client.LRange(ctx, ProcessingQueueKey, 0, -1).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to inspect processing queue: %w", err)
	}

	recovered := 0
	for _, item := range items {
		job, err := ParseJob(item)
		if err != nil {
			// Corrupt / untyped payload in processing queue -> atomically move to DLQ
			_, _ = corruptToDLQScript.Run(ctx, q.client,
				[]string{ProcessingQueueKey, DeadLetterQueueKey},
				item,
			).Result()
			continue
		}

		// Check if job already reached terminal state
		isTerminal := false
		if q.terminalChecker != nil {
			checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
			term, termErr := q.terminalChecker(checkCtx, job)
			checkCancel()
			if termErr == nil && term {
				isTerminal = true
			}
		}

		// Atomically check lease absence, check/increment attempts, and move job
		res, recErr := q.RecoverJobAtomic(ctx, item, job.ID, q.maxRetries, isTerminal)
		if recErr != nil {
			slog.Error("failed to recover abandoned job", "job_id", job.ID, "error", recErr)
			continue
		}
		if res == 1 {
			slog.Info("re-enqueued recoverable orphaned deployment job with expired lease",
				"job_id", job.ID)
			recovered++
		} else if res == 2 {
			slog.Warn("abandoned job exceeded max retries, moved to DLQ", "job_id", job.ID)
		} else if res == 3 {
			slog.Info("abandoned job is already in terminal state; cleaned up from processing queue",
				"job_id", job.ID, "type", job.Type)
		}
	}

	return recovered, nil
}

// StartJobWorker starts background worker goroutines to consume deployment jobs reliably.
func (q *DeploymentQueue) StartJobWorker(parentCtx context.Context, handler func(ctx context.Context, job Job) error) {
	workerCtx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if recCount, err := q.RecoverAbandonedJobs(recoverCtx); err == nil && recCount > 0 {
		slog.Info("recovered abandoned deployment jobs on startup", "count", recCount)
	}
	recoverCancel()

	workerCount := q.workerCount
	if workerCount <= 0 {
		workerCount = DefaultWorkerCount
	}

	for i := 0; i < workerCount; i++ {
		workerIdx := i + 1
		q.wg.Add(1)
		go func(workerID int) {
			defer q.wg.Done()
			workerName := fmt.Sprintf("worker-%d-%s", workerID, uuid.New().String()[:8])
			slog.Info("deployment queue worker started", "worker", workerName)

			for {
				select {
				case <-workerCtx.Done():
					slog.Info("deployment queue worker stopping", "worker", workerName)
					return
				default:
					itemStr, err := q.client.BRPopLPush(workerCtx, DeploymentQueueKey, ProcessingQueueKey, 2*time.Second).Result()
					if err != nil {
						if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
							continue
						}
						slog.Error("redis queue pop error", "worker", workerName, "error", err)
						time.Sleep(1 * time.Second)
						continue
					}

					if itemStr == "" {
						continue
					}

					job, err := ParseJob(itemStr)
					if err != nil {
						slog.Error("invalid job payload in queue, moving to DLQ", "val", itemStr, "error", err)
						// Atomic LREM + LPUSH to prevent double-move by concurrent workers
						_, _ = corruptToDLQScript.Run(context.Background(), q.client,
							[]string{ProcessingQueueKey, DeadLetterQueueKey},
							itemStr,
						).Result()
						continue
					}

					// Acquire execution lease with unique ownership token.
					// acquireLease atomically sets both the lease key and state to "processing".
					leaseToken, acquired, err := q.acquireLease(workerCtx, job.ID, workerName)
					if err != nil {
						slog.Error("failed to check job idempotency / acquire lease", "job_id", job.ID, "error", err)
						continue
					}
					if !acquired {
						// Another worker owns this job. Remove our copy from processing queue,
						// but only remove ONE entry to avoid stealing the other worker's entry.
						// This is safe: BRPopLPush added one entry, we remove that one entry.
						// The other worker's entry (also added by BRPopLPush) remains.
						slog.Warn("skipping duplicate deployment job with active lease", "job_id", job.ID)
						q.client.LRem(context.Background(), ProcessingQueueKey, 1, itemStr)
						continue
					}

					// Context for this specific job execution: cancelled if workerCtx terminates OR if lease is lost!
					jobCtx, cancelJob := context.WithCancel(workerCtx)

					// Heartbeat ticker to continuously extend the lease during long builds/deploys
					hbCtx, cancelHb := context.WithCancel(workerCtx)
					go func() {
						ticker := time.NewTicker(q.heartbeatInterval)
						defer ticker.Stop()
						for {
							select {
							case <-hbCtx.Done():
								return
							case <-ticker.C:
								if err := q.extendLease(context.Background(), job.ID, leaseToken); err != nil {
									slog.Warn("failed to extend lease, canceling job execution",
										"job_id", job.ID, "error", err)
									cancelJob()
									return
								}
							}
						}
					}()

					slog.Info("processing deployment job", "worker", workerName, "job_id", job.ID, "job_type", job.Type)
					execErr := handler(jobCtx, job)

					// Stop heartbeat ticker immediately
					cancelHb()
					cancelJob()

					// Atomic lease-fenced mutations:
					// Every state change (requeue, DLQ, finalization, terminal cleanup) is an atomic
					// Lua script that verifies lease ownership before performing any mutation.
					// If the worker lost its lease to another worker or expiration, the script safely aborts (no-op).
					if execErr != nil {
						slog.Error("deployment job execution failed",
							"job_id", job.ID,
							"job_type", job.Type,
							"error", execErr,
						)

						// Check if job is in a terminal state: NEVER retry a terminal ServiceDeployment!
						isTerminal := false
						if q.terminalChecker != nil {
							checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
							term, err := q.terminalChecker(checkCtx, job)
							checkCancel()
							if err == nil && term {
								isTerminal = true
							}
						}

						if isTerminal {
							slog.Info("deployment job is already in terminal state; performing atomic terminal cleanup",
								"job_id", job.ID, "worker", workerName)
							ok, err := q.TerminalCleanup(context.Background(), job.ID, leaseToken, itemStr, true)
							if err != nil || !ok {
								slog.Warn("worker lost lease; skipped terminal cleanup to protect replacement worker",
									"worker", workerName, "job_id", job.ID, "err", err)
							}
						} else {
							// Check current attempts to decide whether next attempt exceeds maxRetries
							attempts, _ := q.GetAttempts(context.Background(), job.ID)
							if attempts+1 < q.maxRetries {
								slog.Warn("re-queueing failed deployment for retry",
									"job_id", job.ID,
									"current_attempts", attempts,
									"backoff", q.retryBackoff,
								)
								time.Sleep(q.retryBackoff)
								newAtt, ok, err := q.FailAndRequeue(context.Background(), job.ID, leaseToken, itemStr)
								if err != nil || !ok {
									slog.Warn("worker lost lease; aborted requeue to protect replacement worker",
										"worker", workerName, "job_id", job.ID, "err", err)
								} else {
									slog.Info("re-queued failed deployment job", "job_id", job.ID, "new_attempts", newAtt)
								}
							} else {
								slog.Error("deployment job exceeded max retry attempts; moving to dead-letter queue",
									"job_id", job.ID,
									"current_attempts", attempts,
								)
								newAtt, ok, err := q.FailToDLQ(context.Background(), job.ID, leaseToken, itemStr)
								if err != nil || !ok {
									slog.Warn("worker lost lease; aborted DLQ transfer to protect replacement worker",
										"worker", workerName, "job_id", job.ID, "err", err)
								} else {
									slog.Info("transferred failed deployment to DLQ", "job_id", job.ID, "final_attempts", newAtt)
								}
							}
						}
					} else {
						slog.Info("deployment job completed successfully; performing atomic finalization",
							"worker", workerName, "job_id", job.ID)
						ok, err := q.FinalizeSuccess(context.Background(), job.ID, leaseToken, itemStr)
						if err != nil || !ok {
							slog.Warn("worker lost lease; aborted finalization to protect replacement worker",
								"worker", workerName, "job_id", job.ID, "err", err)
						}
					}
				}
			}
		}(workerIdx)
	}
}

// StartWorker is the backward-compatible entrypoint that adapts an ID-based handler.
func (q *DeploymentQueue) StartWorker(parentCtx context.Context, handler func(ctx context.Context, deploymentID uuid.UUID) error) {
	q.StartJobWorker(parentCtx, func(ctx context.Context, job Job) error {
		return handler(ctx, job.ID)
	})
}

// Stop gracefully shuts down the worker goroutines.
func (q *DeploymentQueue) Stop() {
	if q.cancel != nil {
		q.cancel()
	}
	q.wg.Wait()
	slog.Info("deployment queue worker shut down complete")
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
