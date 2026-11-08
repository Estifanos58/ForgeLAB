/**
 * ForgeLAB Centralized Session Manager
 *
 * Implements cross-browser-tab session coordination using Web Locks and BroadcastChannel
 * with a safe in-memory fallback.
 * Prevents simultaneous refresh requests from rotating the same token and accidentally
 * invalidating authentication cookies.
 */

export interface SessionEvent {
  type: 'REFRESH_SUCCESS' | 'REFRESH_FAILED' | 'SESSION_EXPIRED';
  timestamp: number;
}

export interface SessionFetchOptions extends RequestInit {
  _retry?: boolean;
}

export const REFRESH_COOLDOWN_MS = 2500;
export const ACTIVITY_UPDATE_INTERVAL_MS = 2 * 60 * 1000; // 2 minutes between proactive session touches
export const INACTIVITY_TIMEOUT_MS = 24 * 60 * 60 * 1000; // 24 hours before considering completely idle (extended multi-day session)
export const USER_ACTIVITY_THROTTLE_MS = 30 * 1000; // Throttle direct DOM user events to at most once per 30s
export const WEBSOCKET_ACTIVITY_THROTTLE_MS = 5 * 1000; // Throttle WebSocket activity updates to at most once per 5s
const LOCK_NAME = 'forgelab_auth_refresh_lock';
const BROADCAST_CHANNEL_NAME = 'forgelab_session_channel';
const LOCAL_STORAGE_KEY = 'forgelab_last_refresh_timestamp';

export class SessionManager {
  private localLastRefreshTime = 0;
  private lastActivityTime = Date.now();
  private lastSessionUpdateTime = 0;
  private lastUserEventTime = 0;
  private lastWebSocketEventTime = 0;
  private activeWebSocket: WebSocket | null = null;
  private activeRefreshPromise: Promise<void> | null = null;
  private activeTouchPromise: Promise<void> | null = null;
  private broadcastChannel: BroadcastChannel | null = null;
  private listeners: Set<(event: SessionEvent) => void> = new Set();
  private keepAliveTimer: any = null;
  private userActivityCleanup: (() => void) | null = null;

  constructor() {
    this.initBroadcastChannel();
    this.initStorageListener();
    this.initUserActivityListeners();
    this.initKeepAliveTimer();
  }

  private initBroadcastChannel() {
    if (typeof window !== 'undefined' && typeof BroadcastChannel !== 'undefined') {
      try {
        this.broadcastChannel = new BroadcastChannel(BROADCAST_CHANNEL_NAME);
        this.broadcastChannel.onmessage = (event) => {
          const data = event.data as SessionEvent;
          if (data && typeof data.timestamp === 'number') {
            if (data.type === 'REFRESH_SUCCESS') {
              this.localLastRefreshTime = Math.max(this.localLastRefreshTime, data.timestamp);
            }
            this.notifyListeners(data);
          }
        };
      } catch {
        // BroadcastChannel unavailable in this environment
      }
    }
  }

  private initStorageListener() {
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      try {
        window.addEventListener('storage', (event: StorageEvent) => {
          if (event.key === LOCAL_STORAGE_KEY && event.newValue) {
            const ts = Number(event.newValue) || 0;
            if (ts > this.localLastRefreshTime) {
              this.localLastRefreshTime = ts;
              this.lastSessionUpdateTime = Math.max(this.lastSessionUpdateTime, ts);
              this.notifyListeners({ type: 'REFRESH_SUCCESS', timestamp: ts });
            }
          }
        });
      } catch {
        // Storage event listener unavailable
      }
    }
  }

  private initUserActivityListeners() {
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      try {
        const events = ['mousedown', 'keydown', 'scroll', 'touchstart'];
        const handler = () => this.recordActivity('user');
        events.forEach((evt) => window.addEventListener(evt, handler, { passive: true }));
        this.userActivityCleanup = () => {
          events.forEach((evt) => window.removeEventListener(evt, handler));
        };
      } catch {
        // Window events unavailable
      }
    }
  }

  private initKeepAliveTimer() {
    if (typeof window !== 'undefined' && typeof window.setInterval === 'function') {
      try {
        this.keepAliveTimer = setInterval(() => {
          const now = Date.now();
          const isRecentlyActive = now - this.lastActivityTime < INACTIVITY_TIMEOUT_MS;
          const isUpdateDue = now - Math.max(this.lastSessionUpdateTime, this.localLastRefreshTime) >= ACTIVITY_UPDATE_INTERVAL_MS;

          if (isRecentlyActive && isUpdateDue) {
            this.touchSession().catch(() => {});
          }
        }, 60 * 1000);
      } catch {
        // Interval unavailable
      }
    }
  }

  public subscribe(listener: (event: SessionEvent) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private notifyListeners(event: SessionEvent) {
    for (const listener of this.listeners) {
      try {
        listener(event);
      } catch (err) {
        console.error('[SessionManager] Listener error:', err);
      }
    }
  }

  private broadcast(event: SessionEvent) {
    this.notifyListeners(event);
    if (this.broadcastChannel) {
      try {
        this.broadcastChannel.postMessage(event);
      } catch {
        // Broadcast failed
      }
    }
  }

  private getStoredTimestamp(): number {
    if (typeof localStorage !== 'undefined') {
      try {
        const val = localStorage.getItem(LOCAL_STORAGE_KEY);
        return val ? Number(val) || 0 : 0;
      } catch {
        return 0;
      }
    }
    return 0;
  }

  private setStoredTimestamp(ts: number) {
    if (typeof localStorage !== 'undefined') {
      try {
        localStorage.setItem(LOCAL_STORAGE_KEY, String(ts));
      } catch {}
    }
  }

  /**
   * Cross-tab mutex using localStorage with heartbeats and expiry.
   * Used when Web Locks API (navigator.locks) is unavailable in the environment.
   */
  private async withStorageLock<T>(fn: () => Promise<T>): Promise<T> {
    if (typeof localStorage === 'undefined') {
      return await fn();
    }

    const lockKey = 'forgelab_auth_refresh_lock_token';
    const lockTtlMs = 4000;
    const pollIntervalMs = 50;
    const maxWaitMs = 5000;
    const clientId = Math.random().toString(36).substring(2) + '_' + Date.now();
    const startTime = Date.now();

    const tryAcquire = (): boolean => {
      try {
        const raw = localStorage.getItem(lockKey);
        const now = Date.now();
        if (raw) {
          try {
            const parsed = JSON.parse(raw);
            if (now - (parsed.ts || 0) < lockTtlMs && parsed.id !== clientId) {
              return false; // lock held by another tab
            }
          } catch {
            // malformed lock entry; reclaim
          }
        }
        localStorage.setItem(lockKey, JSON.stringify({ id: clientId, ts: now }));
        // Read back to ensure no race condition
        const verify = localStorage.getItem(lockKey);
        if (verify) {
          const parsed = JSON.parse(verify);
          return parsed.id === clientId;
        }
        return true;
      } catch {
        return true;
      }
    };

    const release = () => {
      try {
        const raw = localStorage.getItem(lockKey);
        if (raw) {
          const parsed = JSON.parse(raw);
          if (parsed.id === clientId) {
            localStorage.removeItem(lockKey);
          }
        }
      } catch {}
    };

    while (!tryAcquire()) {
      if (Date.now() - startTime >= maxWaitMs) {
        break; // Max wait exceeded; proceed with single-flight tab protection
      }
      await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
    }

    try {
      return await fn();
    } finally {
      release();
    }
  }

  /**
   * Refreshes the session with cross-tab locking and synchronization.
   * If another tab has refreshed the session within REFRESH_COOLDOWN_MS,
   * rechecks the state and returns immediately without a redundant network call.
   */
  public async refreshSession(): Promise<void> {
    const now = Date.now();
    const stored = this.getStoredTimestamp();
    const mostRecent = Math.max(this.localLastRefreshTime, stored);

    // Fast-path: if a refresh occurred very recently, the cookies are already fresh
    if (now - mostRecent < REFRESH_COOLDOWN_MS) {
      return;
    }

    // Single-flight in current tab
    if (this.activeRefreshPromise) {
      return this.activeRefreshPromise;
    }

    const runGuarded = async () => {
      // Recheck after acquiring the lock: another tab may have refreshed while we waited
      const postLockStored = this.getStoredTimestamp();
      const postLockRecent = Math.max(this.localLastRefreshTime, postLockStored);
      if (Date.now() - postLockRecent < REFRESH_COOLDOWN_MS) {
        return;
      }
      await this.performRefreshCall();
    };

    this.activeRefreshPromise = (async () => {
      try {
        // Use Web Locks API if supported in browser
        if (
          typeof navigator !== 'undefined' &&
          'locks' in navigator &&
          typeof navigator.locks?.request === 'function'
        ) {
          await navigator.locks.request(LOCK_NAME, runGuarded);
        } else {
          // Safe fallback for environments without Web Locks (e.g. Node.js tests, SSR, older browsers)
          await this.withStorageLock(runGuarded);
        }
      } finally {
        this.activeRefreshPromise = null;
      }
    })();

    return this.activeRefreshPromise;
  }

  private async performRefreshCall(): Promise<void> {
    const refreshUrl = '/api/auth/refresh';
    let res: Response;

    try {
      res = await fetch(refreshUrl, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({}),
      });
    } catch (networkErr) {
      this.broadcast({ type: 'REFRESH_FAILED', timestamp: Date.now() });
      throw networkErr;
    }

    if (!res.ok) {
      let ts = Date.now();
      // Check whether another tab refreshed right around this time
      let stored = this.getStoredTimestamp();
      let recent = Math.max(this.localLastRefreshTime, stored);
      if (ts - recent < REFRESH_COOLDOWN_MS) {
        // Another tab successfully rotated tokens and updated browser cookies!
        // Treat as fresh without broadcasting SESSION_EXPIRED or destroying the session.
        this.broadcast({ type: 'REFRESH_SUCCESS', timestamp: recent });
        return;
      }

      // If 401, wait a short grace window (150ms) to see if a concurrent tab's successful refresh
      // is propagating via localStorage or BroadcastChannel before declaring SESSION_EXPIRED
      if (res.status === 401) {
        await new Promise((resolve) => setTimeout(resolve, 150));
        ts = Date.now();
        stored = this.getStoredTimestamp();
        recent = Math.max(this.localLastRefreshTime, stored);
        if (ts - recent < REFRESH_COOLDOWN_MS) {
          this.broadcast({ type: 'REFRESH_SUCCESS', timestamp: recent });
          return;
        }
      }

      this.broadcast({ type: 'REFRESH_FAILED', timestamp: ts });
      if (res.status === 401) {
        this.broadcast({ type: 'SESSION_EXPIRED', timestamp: ts });
      }
      throw new Error(`Session refresh failed with status ${res.status}`);
    }

    const successTs = Date.now();
    this.localLastRefreshTime = successTs;
    this.lastSessionUpdateTime = successTs;
    this.setStoredTimestamp(successTs);
    this.broadcast({ type: 'REFRESH_SUCCESS', timestamp: successTs });
  }

  /**
   * Preflight verification before starting large file transfers or uploads.
   * Verifies credentials and proactively refreshes if expired before data streaming begins.
   */
  public async preflightUploadSession(): Promise<boolean> {
    try {
      // Lightweight verification through /api/auth/me
      const res = await fetch('/api/auth/me', {
        method: 'GET',
        credentials: 'include',
      });

      if (res.status === 401) {
        // Access token expired: refresh before starting upload
        await this.refreshSession();
        // Verify again
        const retryRes = await fetch('/api/auth/me', {
          method: 'GET',
          credentials: 'include',
        });
        return retryRes.ok;
      }

      return res.ok || res.status === 403 || res.status === 200;
    } catch {
      // Non-fatal if offline or guest mode
      return true;
    }
  }

  /**
   * Retrieves a single-use WebSocket ticket, automatically refreshing the session
   * and retrying once if 401 is encountered.
   */
  public async fetchTicketWithSession(ticketUrl?: string): Promise<string | null> {
    const resolvedUrl =
      ticketUrl ||
      (process.env.NEXT_PUBLIC_BACKEND_PORT &&
      typeof window !== 'undefined' &&
      window.location.port === '3000'
        ? `http://${window.location.hostname}:${process.env.NEXT_PUBLIC_BACKEND_PORT}/api/ws/ticket`
        : '/api/ws/ticket');

    try {
      let res = await fetch(resolvedUrl, {
        method: 'POST',
        credentials: 'include',
      });

      if (res.status === 401) {
        // Access token expired: coordinate refresh and retry ticket request once
        try {
          await this.refreshSession();
          res = await fetch(resolvedUrl, {
            method: 'POST',
            credentials: 'include',
          });
        } catch {
          return null;
        }
      }

      if (res.ok) {
        const data = await res.json();
        return data.ticket || null;
      }
    } catch {
      // Network failure
    }
    return null;
  }

  /**
   * Proactively touches and extends the active session (JWT or guest) on interaction.
   * If the cooldown has not elapsed since the last update, returns immediately without network churn.
   */
  public async touchSession(): Promise<void> {
    const now = Date.now();
    const stored = this.getStoredTimestamp();
    const mostRecent = Math.max(this.localLastRefreshTime, this.lastSessionUpdateTime, stored);

    if (now - mostRecent < REFRESH_COOLDOWN_MS) {
      return;
    }

    // If an active WebSocket connection is open, send a session touch message over WebSocket
    if (this.activeWebSocket && typeof (this.activeWebSocket as any).readyState === 'number') {
      const readyState = (this.activeWebSocket as any).readyState;
      if (readyState === 1) { // 1 = OPEN
        try {
          this.activeWebSocket.send(JSON.stringify({ type: 'touch_session', timestamp: now }));
        } catch {
          // Ignore WS send error
        }
      }
    }

    if (this.activeTouchPromise) {
      return this.activeTouchPromise;
    }

    this.activeTouchPromise = (async () => {
      try {
        const touchUrl = '/api/auth/touch';
        let res: Response;
        try {
          res = await fetch(touchUrl, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'include',
            body: JSON.stringify({}),
          });
        } catch {
          // If touch endpoint is unavailable, fall back to refreshSession
          await this.refreshSession();
          return;
        }

        if (res.ok) {
          const successTs = Date.now();
          this.lastSessionUpdateTime = successTs;
          this.localLastRefreshTime = successTs;
          this.setStoredTimestamp(successTs);
          this.broadcast({ type: 'REFRESH_SUCCESS', timestamp: successTs });
        } else if (res.status === 401) {
          // Try standard token refresh
          await this.refreshSession();
        }
      } finally {
        this.activeTouchPromise = null;
      }
    })();

    return this.activeTouchPromise;
  }

  /**
   * Registers or unregisters the active WebSocket connection with the session manager.
   * This enables real-time bidirectional session keepalives and updates over WebSocket communication.
   */
  public registerWebSocket(ws: WebSocket | null): void {
    this.activeWebSocket = ws;
    if (ws) {
      this.recordActivity('websocket');
    }
  }

  public getActiveWebSocket(): WebSocket | null {
    return this.activeWebSocket;
  }

  /**
   * Handles incoming session update messages received from WebSocket communication.
   * Synchronizes session timestamps across tabs without requiring an extra HTTP roundtrip.
   */
  public handleWebSocketSessionUpdate(timestamp?: number): void {
    const ts = timestamp && timestamp > 0 ? timestamp : Date.now();
    this.lastActivityTime = Math.max(this.lastActivityTime, ts);
    this.lastSessionUpdateTime = Math.max(this.lastSessionUpdateTime, ts);
    this.localLastRefreshTime = Math.max(this.localLastRefreshTime, ts);
    this.setStoredTimestamp(ts);
    this.broadcast({ type: 'REFRESH_SUCCESS', timestamp: ts });
  }

  /**
   * Records an interaction with the application.
   *
   * Receiving logs from a codebase being deployed ('deployment_logs') or communicating
   * over an active WebSocket stream ('websocket') is considered active interaction,
   * keeping the session updated even if the user does not touch the keyboard or mouse.
   */
  public recordActivity(source: 'user' | 'deployment_logs' | 'websocket' = 'user'): void {
    const now = Date.now();
    this.lastActivityTime = now;

    if (source === 'user') {
      if (now - this.lastUserEventTime < USER_ACTIVITY_THROTTLE_MS) {
        return;
      }
      this.lastUserEventTime = now;
    } else if (source === 'websocket') {
      if (now - this.lastWebSocketEventTime < WEBSOCKET_ACTIVITY_THROTTLE_MS) {
        return;
      }
      this.lastWebSocketEventTime = now;
    }

    const timeSinceLastUpdate = now - Math.max(this.lastSessionUpdateTime, this.localLastRefreshTime);
    if (timeSinceLastUpdate >= ACTIVITY_UPDATE_INTERVAL_MS) {
      this.touchSession().catch(() => {});
    }
  }

  public getLastActivityTime(): number {
    return this.lastActivityTime;
  }

  public getLastSessionUpdateTime(): number {
    return this.lastSessionUpdateTime;
  }

  public destroy(): void {
    if (this.keepAliveTimer) {
      clearInterval(this.keepAliveTimer);
      this.keepAliveTimer = null;
    }
    if (this.userActivityCleanup) {
      this.userActivityCleanup();
      this.userActivityCleanup = null;
    }
    if (this.broadcastChannel) {
      try {
        this.broadcastChannel.close();
      } catch {}
      this.broadcastChannel = null;
    }
    this.activeWebSocket = null;
    this.listeners.clear();
  }
}

export const sessionManager = new SessionManager();
