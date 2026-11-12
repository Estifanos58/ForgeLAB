import { sessionManager } from '../auth/session-manager';
import { LogQueryOptions } from './types';

export class ApiClientError extends Error {
  status: number;
  data?: any;
  needs_reauth?: boolean;
  sso_required?: boolean;
  rate_limited?: boolean;
  not_found?: boolean;
  not_connected?: boolean;

  constructor(message: string, status: number, data?: any) {
    super(message);
    this.name = 'ApiClientError';
    this.status = status;
    this.data = data;
    if (typeof data === 'object' && data !== null) {
      this.needs_reauth = Boolean(data.needs_reauth);
      this.sso_required = Boolean(data.sso_required);
      this.rate_limited = Boolean(data.rate_limited);
      this.not_found = Boolean(data.not_found);
      this.not_connected = Boolean(data.not_connected);
    }
  }
}

export interface ApiFetchOptions extends RequestInit {
  _retry?: boolean;
}

export interface UploadProgress {
  loaded: number;
  total: number;
  percent: number;
  speed?: number;
  etaSeconds?: number;
}

export async function apiFetch<T>(endpoint: string, options: ApiFetchOptions = {}): Promise<T> {
  const url = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;

  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };

  if (typeof localStorage !== 'undefined') {
    try {
      const guestId = localStorage.getItem('forgelab_guest_session_id');
      if (guestId && !headers['X-Guest-Session']) {
        headers['X-Guest-Session'] = guestId;
      }
    } catch {}
  }

  if (options.body && !(options.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
  }

  const response = await fetch(url, {
    ...options,
    headers,
    credentials: 'include',
  });

  const guestHeader = response.headers.get('x-guest-session');
  if (guestHeader && typeof localStorage !== 'undefined') {
    try {
      localStorage.setItem('forgelab_guest_session_id', guestHeader);
    } catch {}
  }

  // Handle 401 with a single controlled refresh and retry
  const isAuthEndpoint =
    url.includes('/api/auth/login') ||
    url.includes('/api/auth/register') ||
    url.includes('/api/auth/refresh') ||
    url.includes('/api/auth/logout');

  if (response.status === 401 && !options._retry && !isAuthEndpoint) {
    try {
      await sessionManager.refreshSession();
      // Retry original request exactly once
      return await apiFetch<T>(endpoint, {
        ...options,
        _retry: true,
      });
    } catch {
      // Refresh failed; proceed with original 401 error handling
    }
  }

  if (response.status === 204) {
    return {} as T;
  }

  let data: any;
  const contentType = response.headers.get('content-type');
  if (contentType && contentType.includes('application/json')) {
    data = await response.json();
  } else {
    data = await response.text();
  }

  if (!response.ok) {
    const errorMsg =
      typeof data === 'object' && data?.error ? data.error : `Request failed with status ${response.status}`;
    throw new ApiClientError(errorMsg, response.status, data);
  }

  if (!isAuthEndpoint && !url.includes('/api/auth/touch')) {
    sessionManager.recordActivity('user');
  }

  return data as T;
}

function parseContentDispositionFilename(header: string | null): string {
  if (!header) return '';
  const match = header.match(/filename\*?=(?:UTF-8'')?["']?([^"';]+)["']?/i);
  return match?.[1]?.trim() || '';
}

export async function apiFetchBlob(
  endpoint: string,
  options: ApiFetchOptions = {}
): Promise<{ blob: Blob; filename: string }> {
  const url = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;

  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };

  if (typeof localStorage !== 'undefined') {
    try {
      const guestId = localStorage.getItem('forgelab_guest_session_id');
      if (guestId && !headers['X-Guest-Session']) {
        headers['X-Guest-Session'] = guestId;
      }
    } catch {}
  }

  const response = await fetch(url, {
    ...options,
    headers,
    credentials: 'include',
  });

  const guestHeader = response.headers.get('x-guest-session');
  if (guestHeader && typeof localStorage !== 'undefined') {
    try {
      localStorage.setItem('forgelab_guest_session_id', guestHeader);
    } catch {}
  }

  if (response.status === 401 && !options._retry) {
    try {
      await sessionManager.refreshSession();
      return await apiFetchBlob(endpoint, {
        ...options,
        _retry: true,
      });
    } catch {
      // Refresh failed; proceed with original error handling
    }
  }

  if (!response.ok) {
    const contentType = response.headers.get('content-type') || '';
    let data: any;
    if (contentType.includes('application/json')) {
      data = await response.json().catch(() => null);
    } else {
      data = await response.text().catch(() => '');
    }
    const errorMsg =
      typeof data === 'object' && data?.error ? data.error : `Request failed with status ${response.status}`;
    throw new ApiClientError(errorMsg, response.status, data);
  }

  sessionManager.recordActivity('user');
  const blob = await response.blob();
  const filename = parseContentDispositionFilename(response.headers.get('content-disposition'));
  return { blob, filename };
}

const AGENT_URL = 'http://127.0.0.1:4142';
let currentAgentToken: string | null = null;

export function setAgentToken(token: string | null) {
  currentAgentToken = token;
}

export function getAgentToken(): string | null {
  return currentAgentToken;
}

export interface AgentFetchOptions extends RequestInit {
  timeoutMs?: number;
}

function normalizeHeaders(input?: HeadersInit): Record<string, string> {
  const result: Record<string, string> = {};
  if (!input) {
    return result;
  }
  if (typeof Headers !== 'undefined' && input instanceof Headers) {
    input.forEach((value, key) => {
      result[key] = value;
    });
    return result;
  }
  if (Array.isArray(input)) {
    for (const [key, value] of input) {
      result[key] = value;
    }
    return result;
  }
  return { ...(input as Record<string, string>) };
}

function hasHeaderCaseInsensitive(headers: Record<string, string>, name: string): boolean {
  const lower = name.toLowerCase();
  return Object.keys(headers).some((k) => k.toLowerCase() === lower);
}

function createAbortError(message = 'The operation was aborted'): Error {
  const err = new Error(message);
  err.name = 'AbortError';
  return err;
}

export async function agentFetch<T>(
  endpoint: string,
  options: AgentFetchOptions = {},
  token?: string
): Promise<T> {
  const url = `${AGENT_URL}${endpoint.startsWith('/') ? endpoint : `/${endpoint}`}`;
  const authToken = token || currentAgentToken;
  const { timeoutMs, signal: callerSignal, headers: rawHeaders, ...fetchOptions } = options;

  if (callerSignal?.aborted) {
    throw createAbortError();
  }

  const headers = normalizeHeaders(rawHeaders);
  const hasBody = fetchOptions.body !== undefined && fetchOptions.body !== null;
  const isFormData = typeof FormData !== 'undefined' && fetchOptions.body instanceof FormData;
  if (hasBody && !isFormData && !hasHeaderCaseInsensitive(headers, 'Content-Type')) {
    headers['Content-Type'] = 'application/json';
  }
  if (authToken && !hasHeaderCaseInsensitive(headers, 'Authorization')) {
    headers['Authorization'] = `Bearer ${authToken}`;
  }

  const controller = new AbortController();
  let timedOut = false;
  let callerAborted = false;
  let timeoutId: ReturnType<typeof setTimeout> | undefined;

  const onCallerAbort = () => {
    callerAborted = true;
    controller.abort();
  };

  if (callerSignal) {
    callerSignal.addEventListener('abort', onCallerAbort, { once: true });
  }

  if (typeof timeoutMs === 'number' && timeoutMs > 0) {
    timeoutId = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
  }

  try {
    const response = await fetch(url, {
      ...fetchOptions,
      headers,
      signal: controller.signal,
    });

    if (response.status === 204) {
      return {} as T;
    }

    const rawText = await response.text();
    let parsed: any;
    if (rawText) {
      try {
        parsed = JSON.parse(rawText);
      } catch {
        parsed = undefined;
      }
    }

    if (!response.ok) {
      const errorMsg =
        (typeof parsed === 'object' && parsed !== null && (parsed.error || parsed.message)) ||
        rawText.trim() ||
        `Agent request failed with status ${response.status}`;
      throw new ApiClientError(
        errorMsg,
        response.status,
        typeof parsed === 'object' && parsed !== null ? parsed : rawText
      );
    }

    if (!rawText) {
      return {} as T;
    }
    if (parsed !== undefined) {
      return parsed as T;
    }
    throw new ApiClientError('Invalid JSON response from ForgeLAB Local Agent', response.status, rawText);
  } catch (err: any) {
    if (timedOut) {
      throw new ApiClientError(
        `Local Agent request timed out after ${timeoutMs}ms`,
        408,
        { timeout: true, reason: 'timeout' }
      );
    }
    if (callerAborted || callerSignal?.aborted) {
      if (err?.name === 'AbortError') {
        throw err;
      }
      throw createAbortError();
    }
    if (err instanceof ApiClientError) {
      throw err;
    }
    throw new ApiClientError(
      err?.message || 'Unable to reach ForgeLAB Local Agent',
      0,
      { unreachable: true, reason: 'unreachable' }
    );
  } finally {
    if (timeoutId !== undefined) {
      clearTimeout(timeoutId);
    }
    if (callerSignal) {
      callerSignal.removeEventListener('abort', onCallerAbort);
    }
  }
}

export function buildLogQuery(options?: LogQueryOptions | string): string {
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
