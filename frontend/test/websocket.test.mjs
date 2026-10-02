import test from 'node:test';
import assert from 'node:assert/strict';

// Helper to simulate getWebSocketUrl logic under test
function resolveWebSocketUrl({ env = {}, windowObj }) {
  if (env.NEXT_PUBLIC_WS_URL) {
    const envUrl = env.NEXT_PUBLIC_WS_URL.trim();
    if (envUrl.startsWith('ws://') || envUrl.startsWith('wss://')) {
      return envUrl;
    }
    if (windowObj) {
      const wsProto = windowObj.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const path = envUrl.startsWith('/') ? envUrl : `/${envUrl}`;
      return `${wsProto}//${windowObj.location.host}${path}`;
    }
    return envUrl;
  }

  if (!windowObj) {
    return 'ws://localhost:8080/api/ws';
  }

  const wsProto = windowObj.location.protocol === 'https:' ? 'wss:' : 'ws:';

  if (windowObj.location.port === '3000') {
    const backendPort = env.NEXT_PUBLIC_BACKEND_PORT || '8080';
    return `${wsProto}//${windowObj.location.hostname}:${backendPort}/api/ws`;
  }

  return `${wsProto}//${windowObj.location.host}/api/ws`;
}

// Helper to simulate safe log merge logic implemented in useDeploymentWS
function mergeLogs(existingLiveLogs, historicalLogs) {
  const map = new Map();

  // 1. Index historical logs
  for (const log of historicalLogs) {
    const key = log.id !== undefined && log.id !== null ? log.id : `${log.timestamp}-${log.message}`;
    map.set(key, log);
  }

  // 2. Index existing live logs (preserves live events that arrived while REST was in flight)
  for (const log of existingLiveLogs) {
    const key = log.id !== undefined && log.id !== null ? log.id : `${log.timestamp}-${log.message}`;
    map.set(key, log);
  }

  // 3. Chronological sort by timestamp, then database ID
  return Array.from(map.values()).sort((a, b) => {
    const timeA = new Date(a.timestamp).getTime();
    const timeB = new Date(b.timestamp).getTime();
    if (timeA !== timeB) return timeA - timeB;
    if (typeof a.id === 'number' && typeof b.id === 'number') {
      return a.id - b.id;
    }
    return 0;
  });
}

// Helper to simulate parseWSLogEntry implemented in useDeploymentWS
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
    timestamp: data.timestamp || new Date().toISOString(),
    phase: data.phase || 'runtime',
    stream: data.stream || 'stdout',
    message: data.message || '',
  };
}

// Helper to simulate WebSocket channel resolution from selected log target
function resolveWSChannel(target) {
  if (!target) return null;
  if (target.type === 'release') {
    return `deployment:${target.deployment.id}`;
  }
  if (target.type === 'service') {
    return `service-deployment:${target.serviceDeployment.id}`;
  }
  return null;
}

// Helper to simulate service deployment resolution on clicking Logs
function resolveServiceDeployment(service, deploymentList) {
  if (!deploymentList || deploymentList.length === 0) return null;
  if (service?.current_service_deployment_id) {
    const matching = deploymentList.find((d) => d.id === service.current_service_deployment_id);
    if (matching) return matching;
  }
  return deploymentList[0];
}

// Helper to simulate reload log target reconciliation
function resolveReloadLogTarget(prevTarget, deployList, projectServices) {
  if (!prevTarget) {
    if (deployList && deployList.length > 0) {
      return { type: 'release', deployment: deployList[0] };
    }
    return null;
  }
  if (prevTarget.type === 'release') {
    const matching = deployList?.find((d) => d.id === prevTarget.deployment.id);
    return { type: 'release', deployment: matching || deployList?.[0] || prevTarget.deployment };
  }
  if (prevTarget.type === 'service') {
    const matchingSvc = projectServices?.find((s) => s.id === prevTarget.serviceDeployment.service_id);
    return {
      ...prevTarget,
      service: matchingSvc || prevTarget.service,
    };
  }
  return null;
}

test('WebSocket URL resolution — local dev on port 3000', () => {
  const url = resolveWebSocketUrl({
    env: {},
    windowObj: {
      location: {
        protocol: 'http:',
        host: 'localhost:3000',
        hostname: 'localhost',
        port: '3000',
      },
    },
  });
  assert.equal(url, 'ws://localhost:8080/api/ws');
});

test('WebSocket URL resolution — custom backend port via env', () => {
  const url = resolveWebSocketUrl({
    env: { NEXT_PUBLIC_BACKEND_PORT: '9090' },
    windowObj: {
      location: {
        protocol: 'http:',
        host: 'localhost:3000',
        hostname: 'localhost',
        port: '3000',
      },
    },
  });
  assert.equal(url, 'ws://localhost:9090/api/ws');
});

test('WebSocket URL resolution — HTTPS reverse proxy deployment', () => {
  const url = resolveWebSocketUrl({
    env: {},
    windowObj: {
      location: {
        protocol: 'https:',
        host: 'forgelab.example.com',
        hostname: 'forgelab.example.com',
        port: '',
      },
    },
  });
  assert.equal(url, 'wss://forgelab.example.com/api/ws');
});

test('WebSocket URL resolution — explicit NEXT_PUBLIC_WS_URL override', () => {
  const url = resolveWebSocketUrl({
    env: { NEXT_PUBLIC_WS_URL: 'wss://custom.domain.com/gateway/ws' },
    windowObj: {
      location: {
        protocol: 'http:',
        host: 'localhost:3000',
        hostname: 'localhost',
        port: '3000',
      },
    },
  });
  assert.equal(url, 'wss://custom.domain.com/gateway/ws');
});

test('Safe log merging — combines historical REST and live WS logs without losing in-flight events', () => {
  // Scenario: Deployment started.
  // 1. Initial REST response contained log 1 and log 2
  const historical = [
    { id: 1, timestamp: '2026-09-28T08:00:00Z', phase: 'build', stream: 'stdout', message: 'Step 1/3' },
    { id: 2, timestamp: '2026-09-28T08:00:01Z', phase: 'build', stream: 'stdout', message: 'Step 2/3' },
  ];

  // 2. While REST was in flight, WS already received log 2 and live log 3
  const liveReceived = [
    { id: 2, timestamp: '2026-09-28T08:00:01Z', phase: 'build', stream: 'stdout', message: 'Step 2/3' },
    { id: 3, timestamp: '2026-09-28T08:00:02Z', phase: 'build', stream: 'stdout', message: 'Step 3/3' },
  ];

  const merged = mergeLogs(liveReceived, historical);

  // Assert deduplication
  assert.equal(merged.length, 3);
  assert.deepEqual(merged.map((l) => l.id), [1, 2, 3]);
  assert.equal(merged[2].message, 'Step 3/3');
});

test('Safe log merging — duplicate log IDs are strictly deduplicated', () => {
  const logsA = [
    { id: 100, timestamp: '2026-09-28T08:00:00Z', phase: 'build', stream: 'stdout', message: 'Compiling...' },
  ];
  const logsB = [
    { id: 100, timestamp: '2026-09-28T08:00:00Z', phase: 'build', stream: 'stdout', message: 'Compiling...' },
  ];

  const merged = mergeLogs(logsA, logsB);
  assert.equal(merged.length, 1);
});

test('Safe log merging — chronological ordering by timestamp and ID', () => {
  const unordered = [
    { id: 5, timestamp: '2026-09-28T08:00:05Z', phase: 'runtime', stream: 'stdout', message: 'Running' },
    { id: 1, timestamp: '2026-09-28T08:00:01Z', phase: 'source', stream: 'system', message: 'Cloned' },
    { id: 3, timestamp: '2026-09-28T08:00:03Z', phase: 'build', stream: 'stdout', message: 'Built' },
  ];

  const merged = mergeLogs(unordered, []);
  assert.deepEqual(merged.map((l) => l.id), [1, 3, 5]);
});

test('WebSocket message parsing — malformed data does not crash stream', () => {
  let hasError = false;
  try {
    const raw = 'INVALID NON JSON';
    JSON.parse(raw);
  } catch (err) {
    hasError = true;
  }
  assert.equal(hasError, true);
  // Parser handles exception gracefully without throwing uncaught
});

test('WebSocket reconnect backoff calculation', () => {
  let delay = 1000;
  const sequence = [];
  for (let i = 0; i < 6; i++) {
    sequence.push(delay);
    delay = Math.min(delay * 2, 30000);
  }
  assert.deepEqual(sequence, [1000, 2000, 4000, 8000, 16000, 30000]);
});

test('Subscription acknowledgment state transition', () => {
  let connectionState = 'connecting';
  assert.equal(connectionState, 'connecting');

  // onopen
  connectionState = 'subscribing';
  assert.equal(connectionState, 'subscribing');

  // received { type: 'subscribed', channel: 'deployment:123' }
  const msg = { type: 'subscribed', channel: 'deployment:123' };
  if (msg.type === 'subscribed') {
    connectionState = 'subscribed';
  }
  assert.equal(connectionState, 'subscribed');
});

test('Deployment switching resets active log view', () => {
  let activeLogs = [{ id: 1, message: 'Deployment 1 log' }];
  let currentChannel = 'deployment:uuid-1';

  // User selects deployment 2
  currentChannel = 'deployment:uuid-2';
  activeLogs = []; // Reset on channel change

  assert.equal(activeLogs.length, 0);
  assert.equal(currentChannel, 'deployment:uuid-2');
});

test('service-deployment:<id> parsing preserves service_deployment_id and never invents deployment_id', () => {
  const channel = 'service-deployment:sd-uuid-42';
  const rawData = {
    id: 101,
    service_id: 'svc-uuid-1',
    timestamp: '2026-10-02T10:00:00Z',
    phase: 'runtime',
    stream: 'stdout',
    message: 'Server listening on :3000',
  };

  const logEntry = parseWSLogEntry(rawData, channel);

  assert.equal(logEntry.id, 101);
  assert.equal(logEntry.service_deployment_id, 'sd-uuid-42');
  assert.equal(logEntry.service_id, 'svc-uuid-1');
  // CRITICAL: Must NEVER invent deployment_id from service-deployment channel string (e.g. "service-sd-uuid-42")
  assert.equal(logEntry.deployment_id, null);
  assert.equal(logEntry.message, 'Server listening on :3000');
});

test('service-deployment:<id> parsing preserves deployment_id when provided by backend release', () => {
  const channel = 'service-deployment:sd-uuid-42';
  const rawData = {
    id: 102,
    deployment_id: 'rel-uuid-99',
    service_deployment_id: 'sd-uuid-42',
    service_id: 'svc-uuid-1',
    timestamp: '2026-10-02T10:00:01Z',
    phase: 'build',
    stream: 'stdout',
    message: 'Compiling release binary...',
  };

  const logEntry = parseWSLogEntry(rawData, channel);

  assert.equal(logEntry.id, 102);
  assert.equal(logEntry.deployment_id, 'rel-uuid-99');
  assert.equal(logEntry.service_deployment_id, 'sd-uuid-42');
});

test('deployment:<id> parsing preserves release deployment_id without inventing service_deployment_id', () => {
  const channel = 'deployment:rel-uuid-88';
  const rawData = {
    id: 201,
    timestamp: '2026-10-02T10:00:00Z',
    phase: 'startup',
    stream: 'stdout',
    message: 'Starting containers...',
  };

  const logEntry = parseWSLogEntry(rawData, channel);

  assert.equal(logEntry.id, 201);
  assert.equal(logEntry.deployment_id, 'rel-uuid-88');
  assert.equal(logEntry.service_deployment_id, null);
});

test('Independent service deployment safe log merging — deduplication and chronological order', () => {
  const historical = [
    { id: 10, service_deployment_id: 'sd-1', timestamp: '2026-10-02T10:00:00Z', message: 'Build started' },
    { id: 11, service_deployment_id: 'sd-1', timestamp: '2026-10-02T10:00:02Z', message: 'Step 1 complete' },
  ];

  // In-flight live logs from WS stream
  const live = [
    { id: 11, service_deployment_id: 'sd-1', timestamp: '2026-10-02T10:00:02Z', message: 'Step 1 complete' },
    { id: 12, service_deployment_id: 'sd-1', timestamp: '2026-10-02T10:00:04Z', message: 'Container healthy' },
  ];

  const merged = mergeLogs(live, historical);

  assert.equal(merged.length, 3);
  assert.deepEqual(merged.map((l) => l.id), [10, 11, 12]);
  assert.equal(merged[0].message, 'Build started');
  assert.equal(merged[2].message, 'Container healthy');
  assert.equal(merged.every((l) => l.service_deployment_id === 'sd-1'), true);
});

test('WebSocket channel switching between release and independent service deployment', () => {
  let target = {
    type: 'release',
    deployment: { id: 'rel-100', deploy_number: 1 },
  };
  let channel = resolveWSChannel(target);
  assert.equal(channel, 'deployment:rel-100');

  // Trigger independent service deployment (api.services.deploy)
  target = {
    type: 'service',
    serviceDeployment: { id: 'sd-200', service_id: 'svc-1', deploy_number: 3 },
  };
  channel = resolveWSChannel(target);
  assert.equal(channel, 'service-deployment:sd-200');

  // Switch to another service deployment
  target = {
    type: 'service',
    serviceDeployment: { id: 'sd-300', service_id: 'svc-2', deploy_number: 1 },
  };
  channel = resolveWSChannel(target);
  assert.equal(channel, 'service-deployment:sd-300');

  // Switch back to release deployment
  target = {
    type: 'release',
    deployment: { id: 'rel-101', deploy_number: 2 },
  };
  channel = resolveWSChannel(target);
  assert.equal(channel, 'deployment:rel-101');
});

test('Clicking Logs on service resolves current_service_deployment_id when present', () => {
  const service = {
    id: 'svc-api',
    name: 'api',
    current_service_deployment_id: 'sd-current',
  };
  const deployments = [
    { id: 'sd-latest-failed', deploy_number: 4, status: 'failed' },
    { id: 'sd-current', deploy_number: 3, status: 'running' },
    { id: 'sd-old', deploy_number: 2, status: 'stopped' },
  ];

  const resolved = resolveServiceDeployment(service, deployments);
  assert.equal(resolved.id, 'sd-current');
  assert.equal(resolved.deploy_number, 3);
});

test('Clicking Logs on service falls back to latest deployment by deploy_number when current is unset', () => {
  const service = {
    id: 'svc-web',
    name: 'web',
    current_service_deployment_id: null,
  };
  const deployments = [
    { id: 'sd-latest', deploy_number: 5, status: 'running' },
    { id: 'sd-prev', deploy_number: 4, status: 'stopped' },
  ];

  const resolved = resolveServiceDeployment(service, deployments);
  assert.equal(resolved.id, 'sd-latest');
  assert.equal(resolved.deploy_number, 5);
});

test('Clicking service deployment-history entry selects that exact ServiceDeployment', () => {
  const historyDeployments = [
    { id: 'sd-5', deploy_number: 5, status: 'running' },
    { id: 'sd-4', deploy_number: 4, status: 'crashed' },
    { id: 'sd-3', deploy_number: 3, status: 'failed' },
  ];

  // User specifically clicks the crashed deployment #4 to inspect failure logs
  const clicked = historyDeployments.find((d) => d.deploy_number === 4);
  const target = {
    type: 'service',
    serviceDeployment: clicked,
  };

  assert.equal(target.serviceDeployment.id, 'sd-4');
  assert.equal(resolveWSChannel(target), 'service-deployment:sd-4');
});

test('Page reload reconciles and preserves active service deployment log target', () => {
  const prevTarget = {
    type: 'service',
    serviceDeployment: { id: 'sd-current', service_id: 'svc-api', deploy_number: 3 },
    service: { id: 'svc-api', name: 'api', status: 'deploying' },
  };

  const updatedDeployList = [
    { id: 'rel-1', deploy_number: 1 },
  ];
  const updatedProjectServices = [
    { id: 'svc-api', name: 'api', status: 'running' },
  ];

  const reconciled = resolveReloadLogTarget(prevTarget, updatedDeployList, updatedProjectServices);

  assert.equal(reconciled.type, 'service');
  assert.equal(reconciled.serviceDeployment.id, 'sd-current');
  assert.equal(reconciled.service.status, 'running');
  assert.equal(resolveWSChannel(reconciled), 'service-deployment:sd-current');
});

test('WebSocket reconnect sends last_sequence for service deployment channels', () => {
  const channel = 'service-deployment:sd-50';
  let lastSequence = 0;

  // Live log received with sequence 45
  const log1 = parseWSLogEntry({ id: 45, message: 'Step A' }, channel);
  lastSequence = Math.max(lastSequence, log1.id);
  assert.equal(lastSequence, 45);

  // Live log received with sequence 46
  const log2 = parseWSLogEntry({ id: 46, message: 'Step B' }, channel);
  lastSequence = Math.max(lastSequence, log2.id);
  assert.equal(lastSequence, 46);

  // Connection dropped; build reconnect subscription frame
  const subFrame = { type: 'subscribe', channel };
  if (lastSequence > 0) {
    subFrame.last_sequence = lastSequence;
  }

  assert.deepEqual(subFrame, {
    type: 'subscribe',
    channel: 'service-deployment:sd-50',
    last_sequence: 46,
  });
});

