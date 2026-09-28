/**
 * Resolves the WebSocket endpoint URL based on environment configuration, protocol, and hosting topology.
 *
 * Topologies supported:
 * 1. Explicit override: NEXT_PUBLIC_WS_URL (e.g. 'ws://localhost:8080/api/ws', 'wss://api.example.com/api/ws', or relative '/api/ws').
 * 2. Next.js standalone dev server (port 3000): connects to backend on NEXT_PUBLIC_BACKEND_PORT (default '8080') on the current hostname.
 * 3. Unified reverse proxy / production deployment (standard port 80/443 or same origin): connects to same-origin `${wsProto}//${window.location.host}/api/ws`.
 */
export function getWebSocketUrl(): string {
  if (process.env.NEXT_PUBLIC_WS_URL) {
    const envUrl = process.env.NEXT_PUBLIC_WS_URL.trim();
    if (envUrl.startsWith('ws://') || envUrl.startsWith('wss://')) {
      return envUrl;
    }
    if (typeof window !== 'undefined') {
      const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const path = envUrl.startsWith('/') ? envUrl : `/${envUrl}`;
      return `${wsProto}//${window.location.host}${path}`;
    }
    return envUrl;
  }

  if (typeof window === 'undefined') {
    return 'ws://localhost:8080/api/ws';
  }

  const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';

  // Standalone dev server on port 3000 connecting to separate backend port
  if (window.location.port === '3000') {
    const backendPort = process.env.NEXT_PUBLIC_BACKEND_PORT || '8080';
    return `${wsProto}//${window.location.hostname}:${backendPort}/api/ws`;
  }

  // Same-origin reverse proxy / standard port deployment
  return `${wsProto}//${window.location.host}/api/ws`;
}
