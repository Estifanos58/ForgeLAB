import test from 'node:test';
import assert from 'node:assert/strict';
import { ApiClientError, agentFetch } from '../src/lib/api/base.ts';
import {
  agentClient,
  DEFAULT_AGENT_STATUS_TIMEOUT_MS,
  DEFAULT_SELECT_FOLDER_TIMEOUT_MS,
} from '../src/lib/api/agent-client.ts';

// Simulated pollAgentSession implementation reflecting create-project-modal.tsx
function createAgentSessionPoller({ getSourceFn, onProgress, onReady, onStage, onError }) {
  let timer = null;
  let consecutiveFailures = 0;
  const maxConsecutiveFailures = 4;

  const start = (sourceId, token, intervalMs = 20) => {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }

    return new Promise((resolve) => {
      timer = setInterval(async () => {
        try {
          const s = await getSourceFn(sourceId, token);
          consecutiveFailures = 0;

          if (onProgress) {
            onProgress({
              filesScanned: s.files_scanned,
              totalFiles: s.total_files,
              phase: s.phase,
              detectedCount: s.detected_count,
            });
          }

          if (s.status === 'ready' || s.phase === 'ready' || (s.services && s.services.length > 0)) {
            if (timer) clearInterval(timer);
            timer = null;
            if (onReady) onReady(s);
            resolve({ outcome: 'ready', session: s });
            return;
          }

          if (s.status === 'failed' || s.error) {
            if (timer) clearInterval(timer);
            timer = null;
            if (onError) onError(s.error || 'Scan failed');
            resolve({ outcome: 'failed', error: s.error });
            return;
          }

          if (onStage && s.status) {
            onStage(s.status);
          }
        } catch (err) {
          const status = err.status || err.statusCode;
          if (status === 401) {
            if (timer) clearInterval(timer);
            timer = null;
            const msg = 'Agent session is unauthorized or expired. Please re-select the folder.';
            if (onError) onError(msg);
            resolve({ outcome: 'unauthorized', error: msg });
            return;
          }
          if (status === 403) {
            if (timer) clearInterval(timer);
            timer = null;
            const msg = 'Access forbidden: session token does not match or has expired. Please re-select the folder.';
            if (onError) onError(msg);
            resolve({ outcome: 'forbidden', error: msg });
            return;
          }
          if (status === 404) {
            if (timer) clearInterval(timer);
            timer = null;
            const msg = 'Source session not found or expired on the local agent. Please re-select the folder.';
            if (onError) onError(msg);
            resolve({ outcome: 'not_found', error: msg });
            return;
          }

          consecutiveFailures++;
          if (consecutiveFailures >= maxConsecutiveFailures) {
            if (timer) clearInterval(timer);
            timer = null;
            const msg = err.message || 'Lost connection to ForgeLAB Agent during analysis. Please check that the agent is running.';
            if (onError) onError(msg);
            resolve({ outcome: 'network_failed', error: msg, consecutiveFailures });
            return;
          }
        }
      }, intervalMs);
    });
  };

  const stop = () => {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  };

  return { start, stop };
}

test('Local Agent Polling - passes token explicitly and reaches ready', async () => {
  const tokenUsed = [];
  const pollMock = async (sourceId, token) => {
    tokenUsed.push(token);
    return {
      source_id: sourceId,
      status: 'ready',
      phase: 'ready',
      files_scanned: 42,
      total_files: 42,
      detected_count: 2,
      services: [{ name: 'frontend' }, { name: 'backend' }],
    };
  };

  let readyResult = null;
  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
    onReady: (s) => { readyResult = s; },
  });

  const res = await poller.start('source-123', 'tok-abc-exact');
  assert.equal(res.outcome, 'ready');
  assert.equal(tokenUsed[0], 'tok-abc-exact');
  assert.equal(readyResult.status, 'ready');
  assert.equal(readyResult.services.length, 2);
});

test('Local Agent Polling - 403 Forbidden token mismatch stops polling immediately with clear error', async () => {
  let callCount = 0;
  const pollMock = async (sourceId, token) => {
    callCount++;
    const err = new Error('Forbidden');
    err.status = 403;
    throw err;
  };

  let errorCaptured = null;
  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
    onError: (msg) => { errorCaptured = msg; },
  });

  const res = await poller.start('source-456', 'old-stale-token');
  assert.equal(res.outcome, 'forbidden');
  assert.equal(callCount, 1);
  assert.match(errorCaptured, /Access forbidden: session token does not match/);
});

test('Local Agent Polling - 401 Unauthorized stops polling immediately with clear error', async () => {
  let callCount = 0;
  const pollMock = async () => {
    callCount++;
    const err = new Error('Unauthorized');
    err.status = 401;
    throw err;
  };

  let errorCaptured = null;
  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
    onError: (msg) => { errorCaptured = msg; },
  });

  const res = await poller.start('source-789', 'expired-token');
  assert.equal(res.outcome, 'unauthorized');
  assert.equal(callCount, 1);
  assert.match(errorCaptured, /Agent session is unauthorized or expired/);
});

test('Local Agent Polling - 404 Not Found stops polling immediately with clear error', async () => {
  let callCount = 0;
  const pollMock = async () => {
    callCount++;
    const err = new Error('Not Found');
    err.status = 404;
    throw err;
  };

  let errorCaptured = null;
  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
    onError: (msg) => { errorCaptured = msg; },
  });

  const res = await poller.start('source-expired', 'valid-token');
  assert.equal(res.outcome, 'not_found');
  assert.equal(callCount, 1);
  assert.match(errorCaptured, /Source session not found or expired/);
});

test('Local Agent Polling - transient network failures are retried before failing', async () => {
  let callCount = 0;
  const pollMock = async (sourceId, token) => {
    callCount++;
    if (callCount < 3) {
      throw new Error('ECONNRESET transient connection reset');
    }
    return {
      source_id: sourceId,
      status: 'ready',
      phase: 'ready',
      files_scanned: 10,
      total_files: 10,
    };
  };

  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
  });

  const res = await poller.start('source-retry', 'tok-retry');
  assert.equal(res.outcome, 'ready');
  assert.equal(callCount, 3);
});

test('Local Agent Polling - persistent network failure after max failures reports connection error', async () => {
  let callCount = 0;
  const pollMock = async () => {
    callCount++;
    throw new Error('Connection refused to agent');
  };

  let errorCaptured = null;
  const poller = createAgentSessionPoller({
    getSourceFn: pollMock,
    onError: (msg) => { errorCaptured = msg; },
  });

  const res = await poller.start('source-fail', 'tok-fail');
  assert.equal(res.outcome, 'network_failed');
  assert.equal(res.consecutiveFailures, 4);
  assert.equal(callCount, 4);
  assert.match(errorCaptured, /Connection refused to agent/);
});

test('Folder Picker - aborting signal prevents state corruption on cancellation', async () => {
  const abortController = new AbortController();

  let aborted = false;
  const fakeSelectFolder = (title, token, signal) => {
    return new Promise((resolve, reject) => {
      if (signal?.aborted) {
        const err = new Error('This operation was aborted');
        err.name = 'AbortError';
        reject(err);
        return;
      }
      signal?.addEventListener('abort', () => {
        aborted = true;
        const err = new Error('This operation was aborted');
        err.name = 'AbortError';
        reject(err);
      });
    });
  };

  const promise = fakeSelectFolder('Select Folder', 'token-123', abortController.signal);
  abortController.abort();

  await assert.rejects(
    async () => { await promise; },
    { name: 'AbortError' }
  );
  assert.equal(aborted, true);
});

test('Folder Picker - handles 409 Conflict when picker dialog already open', async () => {
  const fakeSelectFolder = async () => {
    const err = new Error('folder picker is already in progress');
    err.status = 409;
    throw err;
  };

  let errorMsg = null;
  let stage = 'selecting';

  try {
    await fakeSelectFolder();
  } catch (err) {
    if (err.status === 409 || err.message?.includes('already in progress')) {
      errorMsg = 'Folder picker dialog is already open on your computer.';
      stage = 'idle';
    }
  }

  assert.equal(stage, 'idle');
  assert.equal(errorMsg, 'Folder picker dialog is already open on your computer.');
});

test('agentFetch & agentClient.getStatus - omits Content-Type on GET requests and uses >= 5s default timeout', async () => {
  assert.ok(DEFAULT_AGENT_STATUS_TIMEOUT_MS >= 5000);
  assert.ok(DEFAULT_SELECT_FOLDER_TIMEOUT_MS >= 120000);

  const originalFetch = globalThis.fetch;
  let capturedInit = null;
  let capturedUrl = null;

  globalThis.fetch = async (url, init) => {
    capturedUrl = url;
    capturedInit = init;
    return new Response(
      JSON.stringify({
        status: 'online',
        version: '1.0.0',
        os: 'windows',
        arch: 'amd64',
        docker_available: false,
        docker_status: 'checking',
        docker: { available: false, status: 'checking' },
        agent_id: '',
        active_sessions: 0,
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } }
    );
  };

  try {
    const status = await agentClient.getStatus();
    assert.equal(capturedUrl, 'http://127.0.0.1:4142/api/agent/status');
    assert.equal(capturedInit.headers['Content-Type'], undefined);
    assert.equal(capturedInit.headers['content-type'], undefined);
    assert.equal(status.status, 'online');
    assert.equal(status.reachable, true);
    assert.equal(status.docker_available, false);
    assert.equal(status.docker_status, 'checking');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('agentFetch - attaches Content-Type: application/json and Authorization on JSON POST', async () => {
  const originalFetch = globalThis.fetch;
  let capturedInit = null;

  globalThis.fetch = async (_url, init) => {
    capturedInit = init;
    return new Response(
      JSON.stringify({ cancelled: true }),
      { status: 200, headers: { 'Content-Type': 'application/json' } }
    );
  };

  try {
    const res = await agentClient.selectFolder('Pick Folder', 'bearer-tok-xyz');
    assert.deepEqual(res, { cancelled: true });
    assert.equal(capturedInit.method, 'POST');
    assert.equal(capturedInit.headers['Content-Type'], 'application/json');
    assert.equal(capturedInit.headers['Authorization'], 'Bearer bearer-tok-xyz');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('agentFetch - distinguishes request timeout (408) from caller AbortSignal and network unreachable', async () => {
  const originalFetch = globalThis.fetch;

  globalThis.fetch = (_url, init) =>
    new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => {
        const err = new Error('aborted');
        err.name = 'AbortError';
        reject(err);
      });
    });

  try {
    // 1. Internal timeout triggers ApiClientError with status 408
    await assert.rejects(
      () => agentFetch('/api/agent/select-folder', { method: 'POST', body: '{}', timeoutMs: 25 }),
      (err) =>
        err instanceof ApiClientError &&
        err.status === 408 &&
        err.data?.timeout === true &&
        err.data?.reason === 'timeout'
    );

    // 2. Explicit caller abort throws AbortError rather than 408 timeout
    const controller = new AbortController();
    setTimeout(() => controller.abort(), 15);
    await assert.rejects(
      () =>
        agentFetch('/api/agent/select-folder', {
          method: 'POST',
          body: '{}',
          timeoutMs: 5000,
          signal: controller.signal,
        }),
      (err) => err?.name === 'AbortError'
    );

    // 3. Network failure distinguishes offline/unreachable agent from online agent without Docker
    globalThis.fetch = async () => {
      throw new TypeError('fetch failed: ECONNREFUSED');
    };
    const offlineStatus = await agentClient.getStatus();
    assert.equal(offlineStatus.status, 'offline');
    assert.equal(offlineStatus.reachable, false);
    assert.match(offlineStatus.error || '', /ECONNREFUSED/);

    // 4. Agent online while Docker stopped stays reachable: true
    globalThis.fetch = async () =>
      new Response(
        JSON.stringify({
          status: 'online',
          version: '1.0.0',
          os: 'windows',
          arch: 'amd64',
          docker_available: false,
          docker_status: 'unavailable',
          docker_error: 'Docker daemon is unreachable',
          agent_id: '',
          active_sessions: 0,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    const noDockerStatus = await agentClient.getStatus();
    assert.equal(noDockerStatus.status, 'online');
    assert.equal(noDockerStatus.reachable, true);
    assert.equal(noDockerStatus.docker_available, false);
    assert.equal(noDockerStatus.docker_status, 'unavailable');
    assert.equal(noDockerStatus.docker_error, 'Docker daemon is unreachable');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('Folder Picker - error responses (408 timeout, 503 non-interactive) never leave stage stuck in selecting', async () => {
  const originalFetch = globalThis.fetch;

  async function runSelectTransition(mockFetch) {
    globalThis.fetch = mockFetch;
    let stage = 'selecting';
    let error = null;
    try {
      await agentClient.selectFolder('Pick', 'tok-123');
      stage = 'ready';
    } catch (err) {
      stage = 'failed';
      error = err?.message || 'failed';
    } finally {
      if (stage === 'selecting') {
        stage = 'failed';
      }
    }
    return { stage, error };
  }

  try {
    const res408 = await runSelectTransition(async () =>
      new Response(JSON.stringify({ error: 'folder picker timed out', reason: 'timeout' }), {
        status: 408,
        headers: { 'Content-Type': 'application/json' },
      })
    );
    assert.equal(res408.stage, 'failed');
    assert.match(res408.error, /timed out/);

    const res503 = await runSelectTransition(async () =>
      new Response(
        JSON.stringify({
          error: 'native folder picker requires an interactive desktop session',
          reason: 'non_interactive_session',
        }),
        { status: 503, headers: { 'Content-Type': 'application/json' } }
      )
    );
    assert.equal(res503.stage, 'failed');
    assert.match(res503.error, /interactive desktop session/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
