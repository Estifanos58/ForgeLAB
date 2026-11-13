import type { NextConfig } from 'next';

const isHttps = process.env.COOKIE_SECURE === 'true' || process.env.ENABLE_HSTS === 'true';
const isDev = process.env.NODE_ENV === 'development';

function extractOrigin(rawUrl?: string): string | null {
  if (!rawUrl) return null;
  const trimmed = rawUrl.trim();
  if (!trimmed || trimmed.startsWith('/')) return null;
  try {
    return new URL(trimmed).origin;
  } catch {
    return null;
  }
}

function toWebSocketOrigin(origin: string): string | null {
  try {
    const u = new URL(origin);
    if (u.protocol === 'http:') {
      u.protocol = 'ws:';
      return u.origin;
    } else if (u.protocol === 'https:') {
      u.protocol = 'wss:';
      return u.origin;
    } else if (u.protocol === 'ws:' || u.protocol === 'wss:') {
      return u.origin;
    }
    return null;
  } catch {
    return null;
  }
}

function buildConnectSrc(): string {
  const sources = new Set<string>([
    "'self'",
    // ForgeLAB Local Agent loopback endpoints (HTTP and WebSocket)
    'http://127.0.0.1:4142',
    'http://localhost:4142',
    'ws://127.0.0.1:4142',
    'ws://localhost:4142',
  ]);

  const backendPort = (process.env.NEXT_PUBLIC_BACKEND_PORT || '8080').trim();
  if (/^\d+$/.test(backendPort)) {
    sources.add(`http://127.0.0.1:${backendPort}`);
    sources.add(`http://localhost:${backendPort}`);
    sources.add(`ws://127.0.0.1:${backendPort}`);
    sources.add(`ws://localhost:${backendPort}`);
  }

  for (const candidate of [
    process.env.NEXT_PUBLIC_APP_URL,
    process.env.NEXT_PUBLIC_API_URL,
    process.env.NEXT_PUBLIC_WS_URL,
    process.env.NEXT_PUBLIC_AGENT_URL,
  ]) {
    const origin = extractOrigin(candidate);
    if (origin) {
      sources.add(origin);
      const wsOrigin = toWebSocketOrigin(origin);
      if (wsOrigin) {
        sources.add(wsOrigin);
      }
    }
  }

  // In development, also permit standard dev ports or localhost WebSocket upgrades if needed.
  // In production, wildcard 'ws:' and 'wss:' are strictly omitted and replaced with explicit trusted origins.
  if (isDev) {
    sources.add('ws://localhost:*');
    sources.add('ws://127.0.0.1:*');
  }

  return Array.from(sources).join(' ');
}

// Content-Security-Policy hardened for Next.js App Router, React 19 hydration, Local Agent, and WebSockets.
// 'unsafe-eval' is omitted outside development builds, and wildcard http:/https: connect-src is disallowed.
const scriptSrc = isDev ? "script-src 'self' 'unsafe-eval' 'unsafe-inline'" : "script-src 'self' 'unsafe-inline'";
const cspHeader = [
  "default-src 'self'",
  scriptSrc,
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: https://avatars.githubusercontent.com https://lh3.googleusercontent.com",
  "font-src 'self' data:",
  `connect-src ${buildConnectSrc()}`,
  "worker-src 'self' blob:",
  "form-action 'self'",
  "frame-ancestors 'none'",
  "object-src 'none'",
  "base-uri 'self'",
].join('; ');

const securityHeaders = [
  { key: 'X-Content-Type-Options', value: 'nosniff' },
  { key: 'X-Frame-Options', value: 'DENY' },
  { key: 'Referrer-Policy', value: 'strict-origin-when-cross-origin' },
  { key: 'Permissions-Policy', value: 'camera=(), microphone=(), geolocation=()' },
  { key: 'Content-Security-Policy', value: cspHeader },
  ...(isHttps
    ? [{ key: 'Strict-Transport-Security', value: 'max-age=31536000; includeSubDomains; preload' }]
    : []),
];

const nextConfig: NextConfig = {
  reactStrictMode: true,
  async headers() {
    return [
      {
        source: '/:path*',
        headers: securityHeaders,
      },
    ];
  },
};

export default nextConfig;
