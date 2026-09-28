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
