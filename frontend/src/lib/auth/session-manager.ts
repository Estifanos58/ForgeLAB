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

const REFRESH_COOLDOWN_MS = 2500;
const LOCK_NAME = 'forgelab_auth_refresh_lock';
const BROADCAST_CHANNEL_NAME = 'forgelab_session_channel';
const LOCAL_STORAGE_KEY = 'forgelab_last_refresh_timestamp';

export class SessionManager {
  private localLastRefreshTime = 0;
  private activeRefreshPromise: Promise<void> | null = null;
  private broadcastChannel: BroadcastChannel | null = null;
  private listeners: Set<(event: SessionEvent) => void> = new Set();

  constructor() {
    this.initBroadcastChannel();
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

    this.activeRefreshPromise = (async () => {
      try {
        // Use Web Locks API if supported in browser
        if (
          typeof navigator !== 'undefined' &&
          'locks' in navigator &&
          typeof navigator.locks?.request === 'function'
        ) {
          await navigator.locks.request(LOCK_NAME, async () => {
            // Recheck after acquiring the lock!
            const postLockStored = this.getStoredTimestamp();
            const postLockRecent = Math.max(this.localLastRefreshTime, postLockStored);
            if (Date.now() - postLockRecent < REFRESH_COOLDOWN_MS) {
              return;
            }
            await this.performRefreshCall();
          });
        } else {
          // Safe fallback for environments without Web Locks (e.g. Node.js tests, SSR)
          await this.performRefreshCall();
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
      const ts = Date.now();
      this.broadcast({ type: 'REFRESH_FAILED', timestamp: ts });
      if (res.status === 401) {
        this.broadcast({ type: 'SESSION_EXPIRED', timestamp: ts });
      }
      throw new Error(`Session refresh failed with status ${res.status}`);
    }

    const successTs = Date.now();
    this.localLastRefreshTime = successTs;
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
}

export const sessionManager = new SessionManager();
