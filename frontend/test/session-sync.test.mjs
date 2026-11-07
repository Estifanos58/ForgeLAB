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
