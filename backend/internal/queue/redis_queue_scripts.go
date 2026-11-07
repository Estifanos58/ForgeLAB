package queue

import "github.com/redis/go-redis/v9"

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

	// reconcileLeaseErrorScript atomically reconciles the state of a job in Redis when an acquireLease
	// call encounters a network or client error.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: deploymentQueueKey, KEYS[4]: stateKey
	// ARGV[1]: leaseToken, ARGV[2]: itemStr, ARGV[3]: canProceed ("1" or "0")
	// Returns:
	//   1: caller holds the lease and can proceed
	//   2: caller held the lease, aborted, released lease and requeued to deployment queue
	//   3: another worker holds active lease; duplicate removed from processing queue only
	//   4: no active lease found; stranded job safely requeued to deployment queue
	reconcileLeaseErrorScript = redis.NewScript(`
		local curLease = redis.call("GET", KEYS[1])
		if curLease == ARGV[1] then
			if ARGV[3] == "1" then
				return 1
			else
				redis.call("DEL", KEYS[1])
				redis.call("LREM", KEYS[2], 1, ARGV[2])
				redis.call("LPUSH", KEYS[3], ARGV[2])
				redis.call("SET", KEYS[4], "pending", "EX", 86400)
				return 2
			end
		elseif curLease and curLease ~= false and curLease ~= "" then
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			return 3
		else
			redis.call("LREM", KEYS[2], 1, ARGV[2])
			redis.call("LPUSH", KEYS[3], ARGV[2])
			redis.call("SET", KEYS[4], "pending", "EX", 86400)
			return 4
		end
	`)

	// removeDuplicateIfLeaseHeldScript handles redundant queue items when acquireLease returns false.
	// If an active lease exists, removes the duplicate item from processing queue.
	// If the lease expired in the interim, restores the job to deployment queue so it does not disappear.
	// KEYS[1]: leaseKey, KEYS[2]: processingQueueKey, KEYS[3]: deploymentQueueKey, KEYS[4]: stateKey
	// ARGV[1]: itemStr
	// Returns:
	//   1: duplicate removed while lease active
	//   2: lease expired; restored to deployment queue
	removeDuplicateIfLeaseHeldScript = redis.NewScript(`
		local curLease = redis.call("GET", KEYS[1])
		if curLease and curLease ~= false and curLease ~= "" then
			redis.call("LREM", KEYS[2], 1, ARGV[1])
			return 1
		else
			redis.call("LREM", KEYS[2], 1, ARGV[1])
			redis.call("LPUSH", KEYS[3], ARGV[1])
			redis.call("SET", KEYS[4], "pending", "EX", 86400)
			return 2
		end
	`)
)
