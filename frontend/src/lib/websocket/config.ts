/**
 * Resolves the WebSocket endpoint URL based on environment configuration, protocol, and hosting topology.
 *
 * Topologies supported:
 * 1. Explicit override: NEXT_PUBLIC_WS_URL (e.g. 'ws://localhost:8080/api/ws', 'wss://api.example.com/api/ws', or relative '/api/ws').
 * 2. Next.js standalone dev server (port 3000): connects to backend on NEXT_PUBLIC_BACKEND_PORT (default '8080') on the current hostname.
 * 3. Unified reverse proxy / production deployment (standard port 80/443 or same origin): connects to same-origin `${wsProto}//${window.location.host}/api/ws`.
 */
export function getWebSocketUrl(): string {
  let resolvedUrl: string;

  if (process.env.NEXT_PUBLIC_WS_URL) {
    const envUrl = process.env.NEXT_PUBLIC_WS_URL.trim();
    if (envUrl.startsWith('ws://') || envUrl.startsWith('wss://')) {
      resolvedUrl = envUrl;
    } else if (typeof window !== 'undefined') {
      const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const path = envUrl.startsWith('/') ? envUrl : `/${envUrl}`;
      resolvedUrl = `${wsProto}//${window.location.host}${path}`;
    } else {
      resolvedUrl = envUrl;
    }
  } else if (typeof window === 'undefined') {
    resolvedUrl = 'ws://localhost:8080/api/ws';
  } else {
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';

    // Standalone dev server on port 3000 connecting to separate backend port
    if (window.location.port === '3000') {
      const backendPort = process.env.NEXT_PUBLIC_BACKEND_PORT || '8080';
      resolvedUrl = `${wsProto}//${window.location.hostname}:${backendPort}/api/ws`;
    } else {
      // Same-origin reverse proxy / standard port deployment
      resolvedUrl = `${wsProto}//${window.location.host}/api/ws`;
    }
  }

  if (typeof window !== 'undefined' && process.env.NODE_ENV === 'development') {
    console.log('[ForgeLAB WS] Resolved WebSocket URL:', resolvedUrl);
  }

  return resolvedUrl;
}
