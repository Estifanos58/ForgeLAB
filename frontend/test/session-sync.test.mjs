import { test } from 'node:test';
import assert from 'node:assert/strict';
import { SessionManager } from '../src/lib/auth/session-manager.js';

test('SessionManager - single flight refresh coalescing', async () => {
  const manager = new SessionManager();
  let fetchCallCount = 0;

  // Mock global fetch
  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/refresh') {
      fetchCallCount++;
      await new Promise((resolve) => setTimeout(resolve, 50));
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    // Launch 5 concurrent refresh requests in the same tab
    const p1 = manager.refreshSession();
    const p2 = manager.refreshSession();
    const p3 = manager.refreshSession();
    const p4 = manager.refreshSession();
    const p5 = manager.refreshSession();

    await Promise.all([p1, p2, p3, p4, p5]);

    // All concurrent requests must share a single network call
    assert.equal(fetchCallCount, 1, 'concurrent refresh requests in same tab should coalesce to 1 call');
  } finally {
    global.fetch = originalFetch;
  }
});

test('SessionManager - fast-path cache prevents redundant refresh within cooldown', async () => {
  const manager = new SessionManager();
  let fetchCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/refresh') {
      fetchCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    await manager.refreshSession();
    assert.equal(fetchCallCount, 1);

    // Immediate second call should hit the cache and not perform network call
    await manager.refreshSession();
    assert.equal(fetchCallCount, 1, 'second refresh call within cooldown should not make a network call');
  } finally {
    global.fetch = originalFetch;
  }
});

test('SessionManager - cross-tab coordination via Web Locks and lock recheck', async () => {
  let lockAcquiredCount = 0;

  // Mock localStorage
  const storage = new Map();
  const originalLocalStorage = global.localStorage;
  global.localStorage = {
    getItem: (key) => storage.get(key) || null,
    setItem: (key, val) => storage.set(key, val),
  };

  // Mock navigator.locks using Object.defineProperty (Node.js navigator is a getter)
  const originalLocks = global.navigator?.locks;
  Object.defineProperty(global.navigator, 'locks', {
    value: {
      request: async (lockName, callback) => {
        lockAcquiredCount++;
        // Simulate another tab finishing refresh right before this tab acquires the lock
        storage.set('forgelab_last_refresh_timestamp', String(Date.now()));
        return callback();
      },
    },
    configurable: true,
  });

  const manager = new SessionManager();
  let fetchCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/refresh') {
      fetchCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    // Initially timestamp is old (> REFRESH_COOLDOWN_MS ago)
    storage.set('forgelab_last_refresh_timestamp', String(Date.now() - 10000));

    await manager.refreshSession();

    assert.equal(lockAcquiredCount, 1, 'Web Lock was requested');
    assert.equal(fetchCallCount, 0, 'network call was skipped because another tab refreshed while lock was acquired');
  } finally {
    if (originalLocks) {
      Object.defineProperty(global.navigator, 'locks', { value: originalLocks, configurable: true });
    } else {
      delete global.navigator.locks;
    }
    global.localStorage = originalLocalStorage;
    global.fetch = originalFetch;
  }
});

test('SessionManager - preflightUploadSession refreshes if access token is expired', async () => {
  const manager = new SessionManager();
  let meCallCount = 0;
  let refreshCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/me') {
      meCallCount++;
      if (meCallCount === 1) {
        // First call fails with 401 (expired)
        return { ok: false, status: 401 };
      }
      // Second call succeeds after refresh
      return {
        ok: true,
        status: 200,
        json: async () => ({ id: 'user-123', email: 'test@forgelab.local' }),
      };
    }
    if (url === '/api/auth/refresh') {
      refreshCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    const verified = await manager.preflightUploadSession();
    assert.equal(verified, true, 'preflight should succeed after token refresh');
    assert.equal(meCallCount, 2, '/api/auth/me called twice (pre and post refresh)');
    assert.equal(refreshCallCount, 1, '/api/auth/refresh called once to recover session');
  } finally {
    global.fetch = originalFetch;
  }
});

test('SessionManager - fetchTicketWithSession recovers from 401', async () => {
  const manager = new SessionManager();
  let ticketCallCount = 0;
  let refreshCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url.includes('/api/ws/ticket')) {
      ticketCallCount++;
      if (ticketCallCount === 1) {
        return { ok: false, status: 401 };
      }
      return {
        ok: true,
        status: 201,
        json: async () => ({ ticket: 'fresh-ticket-uuid' }),
      };
    }
    if (url === '/api/auth/refresh') {
      refreshCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    const ticket = await manager.fetchTicketWithSession('http://localhost:8080/api/ws/ticket');
    assert.equal(ticket, 'fresh-ticket-uuid');
    assert.equal(ticketCallCount, 2, 'ticket requested twice (initial 401 + retry after refresh)');
    assert.equal(refreshCallCount, 1, 'session refresh coordinated');
  } finally {
    global.fetch = originalFetch;
  }
});

test('SessionManager - cross-tab coordination without Web Locks using storage lock', async () => {
  const storage = new Map();
  const originalLocalStorage = global.localStorage;
  global.localStorage = {
    getItem: (key) => storage.get(key) || null,
    setItem: (key, val) => storage.set(key, val),
    removeItem: (key) => storage.delete(key),
  };

  // Ensure navigator.locks is explicitly undefined
  const originalLocks = global.navigator?.locks;
  Object.defineProperty(global.navigator, 'locks', {
    value: undefined,
    configurable: true,
  });

  let fetchCallCount = 0;
  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/refresh') {
      fetchCallCount++;
      // Simulate slow refresh
      await new Promise((resolve) => setTimeout(resolve, 80));
      return {
        ok: true,
        status: 200,
        json: async () => ({ expires_in: 900 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    // Simulate 3 separate tabs (independent SessionManager instances sharing localStorage)
    const tab1 = new SessionManager();
    const tab2 = new SessionManager();
    const tab3 = new SessionManager();

    // Trigger refresh concurrently from all 3 tabs
    await Promise.all([
      tab1.refreshSession(),
      tab2.refreshSession(),
      tab3.refreshSession(),
    ]);

    // Only one tab should have performed the refresh network call; others wait and recheck
    assert.equal(fetchCallCount, 1, 'only 1 refresh network call across tabs without Web Locks');
  } finally {
    if (originalLocks !== undefined) {
      Object.defineProperty(global.navigator, 'locks', { value: originalLocks, configurable: true });
    }
    global.localStorage = originalLocalStorage;
    global.fetch = originalFetch;
  }
});

test('SessionManager - losing 401 refresh does not fire SESSION_EXPIRED when another tab refreshed', async () => {
  const storage = new Map();
  const originalLocalStorage = global.localStorage;
  global.localStorage = {
    getItem: (key) => storage.get(key) || null,
    setItem: (key, val) => storage.set(key, val),
    removeItem: (key) => storage.delete(key),
  };

  let expiredFired = false;
  let successFired = false;

  const manager = new SessionManager();
  manager.subscribe((event) => {
    if (event.type === 'SESSION_EXPIRED') {
      expiredFired = true;
    }
    if (event.type === 'REFRESH_SUCCESS') {
      successFired = true;
    }
  });

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/refresh') {
      // Simulate another tab having refreshed successfully 50ms ago
      storage.set('forgelab_last_refresh_timestamp', String(Date.now() - 50));
      // Tab 2 loses race and gets 401 from backend
      return {
        ok: false,
        status: 401,
        json: async () => ({ error: 'refresh token revoked' }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    // Refresh was attempted
    await manager.refreshSession();

    assert.equal(expiredFired, false, 'SESSION_EXPIRED must NOT fire when another tab already refreshed');
    assert.equal(successFired, true, 'REFRESH_SUCCESS should be acknowledged');
  } finally {
    global.localStorage = originalLocalStorage;
    global.fetch = originalFetch;
  }
});

test('SessionManager - receiving deployment logs is considered interaction and touches session', async () => {
  const manager = new SessionManager();
  let touchCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/touch') {
      touchCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ status: 'ok', authenticated: true, expires_in: 86400 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    const initialActivity = manager.getLastActivityTime();

    await new Promise((r) => setTimeout(r, 10));

    // Simulate logs arriving from running codebase deployment
    manager.recordActivity('deployment_logs');

    assert.ok(manager.getLastActivityTime() >= initialActivity, 'deployment_logs must update activity timestamp');

    // Trigger proactive touch
    await manager.touchSession();
    assert.equal(touchCallCount, 1, 'touchSession should call /api/auth/touch');
    assert.ok(manager.getLastSessionUpdateTime() > 0, 'lastSessionUpdateTime should be updated');
  } finally {
    manager.destroy();
    global.fetch = originalFetch;
  }
});

test('SessionManager - rapid deployment logs are throttled to prevent network storm', async () => {
  const manager = new SessionManager();
  let touchCallCount = 0;

  const originalFetch = global.fetch;
  global.fetch = async (url) => {
    if (url === '/api/auth/touch') {
      touchCallCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({ status: 'ok', authenticated: true, expires_in: 86400 }),
      };
    }
    return { ok: false, status: 404 };
  };

  try {
    // Ingest 50 logs in rapid succession (as during docker build)
    for (let i = 0; i < 50; i++) {
      manager.recordActivity('deployment_logs');
    }

    // Explicit touch during cooldown should be skipped
    await manager.touchSession();
    await manager.touchSession();

    assert.equal(touchCallCount, 1, 'rapid touches within cooldown should coalesce to at most 1 network call');
  } finally {
    manager.destroy();
    global.fetch = originalFetch;
  }
});
