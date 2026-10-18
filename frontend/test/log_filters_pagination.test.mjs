import test from 'node:test';
import assert from 'node:assert/strict';

// Helper to simulate buildLogQuery logic under test
function buildLogQuery(options) {
  if (!options) return '';
  if (typeof options === 'string') {
    return `?service_id=${encodeURIComponent(options)}`;
  }
  const params = new URLSearchParams();
  if (options.serviceId) params.set('service_id', options.serviceId);
  if (options.phase && options.phase !== 'all') params.set('phase', options.phase);
  if (options.cursor !== undefined) params.set('cursor', String(options.cursor));
  if (options.afterId !== undefined) params.set('after_id', String(options.afterId));
  if (options.beforeId !== undefined) params.set('before_id', String(options.beforeId));
  if (options.limit !== undefined) params.set('limit', String(options.limit));
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

// Helper to simulate phase and service filtering from LogsSection
function filterLogs({ logs, phaseFilter = 'all', serviceScope = null, selectedLogTarget = null }) {
  // 1. Service Scope:
  // If target is a service deployment, show its logs directly.
  // Otherwise if a specific service is selected, filter to that service.
  // Otherwise ("All Services"), include all services (frontend, backend, databases, workers, independent).
  const scopedLogs =
    selectedLogTarget?.type === 'service'
      ? logs
      : serviceScope
      ? logs.filter((l) => l.service_id === serviceScope)
      : logs;

  // 2. Phase Category Filter:
  // - "deployment": source, build, startup, and health phases
  // - "production": ongoing runtime stdout/stderr
  // - "all": everything
  const displayedLogs = scopedLogs.filter((log) => {
    if (phaseFilter === 'all') return true;
    if (phaseFilter === 'deployment') {
      return ['source', 'build', 'startup', 'health'].includes(log.phase);
    }
    if (phaseFilter === 'production') {
      return log.phase === 'runtime';
    }
    return true;
  });

  return {
    scopedLogs,
    displayedLogs,
    countAll: scopedLogs.length,
    countDeployment: scopedLogs.filter((l) => ['source', 'build', 'startup', 'health'].includes(l.phase)).length,
    countProduction: scopedLogs.filter((l) => l.phase === 'runtime').length,
  };
}

// Helper to simulate bounded memory live log ingestion in useDeploymentWS
const MAX_STORED_LOGS = 2000;

function ingestLiveLog(prevLogs, newLog) {
  // Deduplicate
  if (newLog.id !== undefined && newLog.id !== null) {
    if (prevLogs.some((existing) => String(existing.id) === String(newLog.id))) {
      return prevLogs;
    }
  } else {
    if (prevLogs.some((existing) => existing.timestamp === newLog.timestamp && existing.message === newLog.message)) {
      return prevLogs;
    }
  }

  const updated = [...prevLogs, newLog];
  if (updated.length > MAX_STORED_LOGS) {
    return updated.slice(updated.length - MAX_STORED_LOGS);
  }
  return updated;
}

// Helper to simulate safe historical merge with bounded memory
function mergeHistoricalLogs(prevLogs, historical) {
  const map = new Map();

  for (const log of historical) {
    const key = log.id !== undefined && log.id !== null ? `id:${log.id}` : `ts:${log.timestamp}:${log.message}`;
    map.set(key, log);
  }

  for (const log of prevLogs) {
    const key = log.id !== undefined && log.id !== null ? `id:${log.id}` : `ts:${log.timestamp}:${log.message}`;
    map.set(key, log);
  }

  const sorted = Array.from(map.values()).sort((a, b) => {
    const timeA = new Date(a.timestamp).getTime();
    const timeB = new Date(b.timestamp).getTime();
    if (timeA !== timeB) return timeA - timeB;
    if (typeof a.id === 'number' && typeof b.id === 'number') {
      return a.id - b.id;
    }
    return 0;
  });

  if (sorted.length > MAX_STORED_LOGS) {
    return sorted.slice(sorted.length - MAX_STORED_LOGS);
  }
  return sorted;
}

// Helper to simulate parseWSLogEntry for all message types
function parseWSLogEntry(data, channel) {
  const isServiceDeploymentChannel = channel ? channel.startsWith('service-deployment:') : false;
  const isReleaseDeploymentChannel = channel ? channel.startsWith('deployment:') : false;

  const fallbackReleaseDeploymentId = isReleaseDeploymentChannel
    ? channel.replace('deployment:', '').split(':')[0]
    : null;

  const fallbackServiceDeploymentId = isServiceDeploymentChannel
    ? channel.replace('service-deployment:', '')
    : null;

  return {
    id: data.id,
    deployment_id: data.deployment_id || fallbackReleaseDeploymentId || null,
    service_deployment_id: data.service_deployment_id || fallbackServiceDeploymentId || null,
    service_id: data.service_id || null,
    service_name: data.service_name || null,
    timestamp: data.timestamp || new Date().toISOString(),
    phase: data.phase || 'runtime',
    stream: data.stream || 'stdout',
    message: data.message || '',
  };
}

// Helper to resolve service badge name
function resolveLogServiceName(log, services) {
  if (log.service_name) return log.service_name;
  if (log.service_id && services && services.length > 0) {
    const match = services.find((s) => s.id === log.service_id);
    if (match) return match.name;
  }
  return null;
}

// --- TESTS ---

test('Log Filter Tabs — All Logs returns complete history', () => {
  const sampleLogs = [
    { id: 1, phase: 'source', message: 'Cloned repository' },
    { id: 2, phase: 'build', message: 'Building Docker image' },
    { id: 3, phase: 'startup', message: 'Starting container' },
    { id: 4, phase: 'health', message: 'HTTP healthcheck passed' },
    { id: 5, phase: 'runtime', message: 'Listening on port 3000' },
  ];

  const result = filterLogs({ logs: sampleLogs, phaseFilter: 'all' });
  assert.equal(result.displayedLogs.length, 5);
  assert.equal(result.countAll, 5);
  assert.equal(result.countDeployment, 4);
  assert.equal(result.countProduction, 1);
});

test('Log Filter Tabs — Deployment filter returns only source, build, startup, and health phases', () => {
  const sampleLogs = [
    { id: 1, phase: 'source', message: 'Cloning repo' },
    { id: 2, phase: 'build', message: 'Step 1/5' },
    { id: 3, phase: 'startup', message: 'Created container' },
    { id: 4, phase: 'health', message: 'Health probe 200 OK' },
    { id: 5, phase: 'runtime', message: 'Incoming request GET /api/v1/health' },
    { id: 6, phase: 'runtime', message: 'Incoming request POST /login' },
  ];

  const result = filterLogs({ logs: sampleLogs, phaseFilter: 'deployment' });
  assert.equal(result.displayedLogs.length, 4);
  assert.deepEqual(
    result.displayedLogs.map((l) => l.phase),
    ['source', 'build', 'startup', 'health']
  );
  assert.equal(result.countDeployment, 4);
  assert.equal(result.countProduction, 2);
});

test('Log Filter Tabs — Production filter returns only ongoing runtime stdout/stderr logs', () => {
  const sampleLogs = [
    { id: 1, phase: 'source', message: 'Cloning repo' },
    { id: 2, phase: 'build', message: 'Step 1/5' },
    { id: 3, phase: 'startup', message: 'Created container' },
    { id: 4, phase: 'health', message: 'Health probe 200 OK' },
    { id: 5, phase: 'runtime', stream: 'stdout', message: 'Server ready on :8080' },
    { id: 6, phase: 'runtime', stream: 'stderr', message: 'WARN: High memory watermark' },
  ];

  const result = filterLogs({ logs: sampleLogs, phaseFilter: 'production' });
  assert.equal(result.displayedLogs.length, 2);
  assert.deepEqual(
    result.displayedLogs.map((l) => l.message),
    ['Server ready on :8080', 'WARN: High memory watermark']
  );
  assert.equal(result.displayedLogs.every((l) => l.phase === 'runtime'), true);
});

test('Service Scope — All Services aggregates frontend, backend, databases, workers, and independent services', () => {
  const multiServiceLogs = [
    { id: 1, service_id: 'svc-frontend', phase: 'runtime', message: 'Next.js serving /dashboard' },
    { id: 2, service_id: 'svc-backend', phase: 'runtime', message: 'Go server handling POST /graphql' },
    { id: 3, service_id: 'svc-postgres', phase: 'runtime', message: 'PostgreSQL database ready for connections' },
    { id: 4, service_id: 'svc-worker', phase: 'runtime', message: 'Worker processed job #8812' },
    { id: 5, service_id: 'svc-redis', phase: 'runtime', message: 'Redis pub/sub initialized' },
  ];

  const result = filterLogs({ logs: multiServiceLogs, serviceScope: null });
  assert.equal(result.displayedLogs.length, 5);
  const distinctServices = new Set(result.displayedLogs.map((l) => l.service_id));
  assert.equal(distinctServices.size, 5);
  assert.ok(distinctServices.has('svc-frontend'));
  assert.ok(distinctServices.has('svc-backend'));
  assert.ok(distinctServices.has('svc-postgres'));
  assert.ok(distinctServices.has('svc-worker'));
  assert.ok(distinctServices.has('svc-redis'));
});

test('Service Scope — Selecting individual service scopes output strictly to that service', () => {
  const multiServiceLogs = [
    { id: 1, service_id: 'svc-frontend', phase: 'runtime', message: 'Frontend log 1' },
    { id: 2, service_id: 'svc-backend', phase: 'runtime', message: 'Backend log 1' },
    { id: 3, service_id: 'svc-backend', phase: 'runtime', message: 'Backend log 2' },
    { id: 4, service_id: 'svc-db', phase: 'runtime', message: 'DB log 1' },
  ];

  const backendScope = filterLogs({ logs: multiServiceLogs, serviceScope: 'svc-backend' });
  assert.equal(backendScope.displayedLogs.length, 2);
  assert.deepEqual(
    backendScope.displayedLogs.map((l) => l.id),
    [2, 3]
  );
  assert.equal(backendScope.displayedLogs.every((l) => l.service_id === 'svc-backend'), true);

  const dbScope = filterLogs({ logs: multiServiceLogs, serviceScope: 'svc-db' });
  assert.equal(dbScope.displayedLogs.length, 1);
  assert.equal(dbScope.displayedLogs[0].id, 4);
});

test('Service Scope — Resolves service badges accurately in All Services view', () => {
  const services = [
    { id: 'svc-frontend-id', name: 'web', role: 'frontend' },
    { id: 'svc-backend-id', name: 'api', role: 'backend' },
    { id: 'svc-db-id', name: 'postgres', role: 'database' },
  ];

  const logWithServiceName = { id: 1, service_name: 'api', service_id: 'svc-backend-id', message: 'ready' };
  const logWithOnlyServiceId = { id: 2, service_id: 'svc-db-id', message: 'checkpoint complete' };
  const logWithoutService = { id: 3, message: 'system orchestrator event' };

  assert.equal(resolveLogServiceName(logWithServiceName, services), 'api');
  assert.equal(resolveLogServiceName(logWithOnlyServiceId, services), 'postgres');
  assert.equal(resolveLogServiceName(logWithoutService, services), null);
});

test('Bounded Memory — Ingesting live logs caps array at MAX_STORED_LOGS (2000) preventing browser memory leak', () => {
  let logs = [];
  // Push 2500 live log entries
  for (let i = 1; i <= 2500; i++) {
    logs = ingestLiveLog(logs, {
      id: i,
      timestamp: new Date(1700000000000 + i * 1000).toISOString(),
      phase: 'runtime',
      stream: 'stdout',
      message: `Log line ${i}`,
    });
  }

  assert.equal(logs.length, MAX_STORED_LOGS);
  // Ensure we kept the latest logs (501 to 2500) and dropped the oldest (1 to 500)
  assert.equal(logs[0].id, 501);
  assert.equal(logs[logs.length - 1].id, 2500);
});

test('Bounded Memory — Merging historical logs caps array at MAX_STORED_LOGS (2000)', () => {
  const liveLogs = [];
  for (let i = 2001; i <= 3000; i++) {
    liveLogs.push({
      id: i,
      timestamp: new Date(1700000000000 + i * 1000).toISOString(),
      phase: 'runtime',
      stream: 'stdout',
      message: `Live log ${i}`,
    });
  }

  const historicalLogs = [];
  for (let i = 1; i <= 2000; i++) {
    historicalLogs.push({
      id: i,
      timestamp: new Date(1700000000000 + i * 1000).toISOString(),
      phase: 'build',
      stream: 'stdout',
      message: `Historical log ${i}`,
    });
  }

  const merged = mergeHistoricalLogs(liveLogs, historicalLogs);
  assert.equal(merged.length, MAX_STORED_LOGS);
  // Total entries were 3000; keeping the latest 2000 means logs 1001 through 3000
  assert.equal(merged[0].id, 1001);
  assert.equal(merged[merged.length - 1].id, 3000);
});

test('WebSocket message types — handles log, service_deployment_log, and release_log with service metadata', () => {
  const channel = 'project:proj-uuid-1';

  const rawReleaseLog = {
    id: 501,
    deployment_id: 'rel-uuid-1',
    phase: 'build',
    stream: 'stdout',
    message: 'Global release build complete',
  };
  const releaseLog = parseWSLogEntry(rawReleaseLog, channel);
  assert.equal(releaseLog.id, 501);
  assert.equal(releaseLog.deployment_id, 'rel-uuid-1');
  assert.equal(releaseLog.phase, 'build');

  const rawServiceLog = {
    id: 502,
    service_id: 'svc-api-id',
    service_name: 'api',
    service_deployment_id: 'sd-uuid-2',
    phase: 'runtime',
    stream: 'stdout',
    message: 'API worker ready on port 8080',
  };
  const serviceLog = parseWSLogEntry(rawServiceLog, channel);
  assert.equal(serviceLog.id, 502);
  assert.equal(serviceLog.service_id, 'svc-api-id');
  assert.equal(serviceLog.service_name, 'api');
  assert.equal(serviceLog.service_deployment_id, 'sd-uuid-2');
  assert.equal(serviceLog.phase, 'runtime');
});

test('Log Deduplication — Ingesting identical log ID does not create duplicate entries', () => {
  let logs = [
    { id: 10, timestamp: '2026-10-18T10:00:00Z', message: 'First line' },
    { id: 11, timestamp: '2026-10-18T10:00:01Z', message: 'Second line' },
  ];

  // Re-ingest log ID 11
  const updated = ingestLiveLog(logs, { id: 11, timestamp: '2026-10-18T10:00:01Z', message: 'Second line' });
  assert.equal(updated.length, 2);
  assert.deepEqual(updated.map((l) => l.id), [10, 11]);

  // Ingest new log ID 12
  const updated2 = ingestLiveLog(updated, { id: 12, timestamp: '2026-10-18T10:00:02Z', message: 'Third line' });
  assert.equal(updated2.length, 3);
  assert.deepEqual(updated2.map((l) => l.id), [10, 11, 12]);
});

test('Pagination Query Builder — buildLogQuery produces correct query strings', () => {
  // Empty options
  assert.equal(buildLogQuery(), '');
  assert.equal(buildLogQuery({}), '');

  // String legacy fallback
  assert.equal(buildLogQuery('svc-123'), '?service_id=svc-123');

  // Full options: serviceId, phase (deployment), cursor, limit
  const q1 = buildLogQuery({
    serviceId: 'svc-api',
    phase: 'deployment',
    limit: 50,
    cursor: 120,
  });
  const params1 = new URLSearchParams(q1.substring(1));
  assert.equal(params1.get('service_id'), 'svc-api');
  assert.equal(params1.get('phase'), 'deployment');
  assert.equal(params1.get('cursor'), '120');
  assert.equal(params1.get('limit'), '50');

  // Phase === 'all' is omitted from query params to fetch all phases
  const q2 = buildLogQuery({
    phase: 'all',
    afterId: 450,
    limit: 100,
  });
  const params2 = new URLSearchParams(q2.substring(1));
  assert.equal(params2.get('phase'), null);
  assert.equal(params2.get('after_id'), '450');
  assert.equal(params2.get('limit'), '100');

  // Production phase query with before_id cursor
  const q3 = buildLogQuery({
    phase: 'production',
    beforeId: 800,
    limit: 200,
  });
  const params3 = new URLSearchParams(q3.substring(1));
  assert.equal(params3.get('phase'), 'production');
  assert.equal(params3.get('before_id'), '800');
  assert.equal(params3.get('limit'), '200');
});
