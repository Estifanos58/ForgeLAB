import test from 'node:test';
import assert from 'node:assert/strict';
import nextConfig from '../next.config.ts';

test('Frontend security headers configuration', async () => {
  const config = nextConfig.default || nextConfig;
  assert.ok(typeof config.headers === 'function', 'config.headers should be a function');
  const routeHeaders = await config.headers();
  assert.ok(Array.isArray(routeHeaders), 'headers() should return an array');
  
  const rootRule = routeHeaders.find((r) => r.source === '/:path*');
  assert.ok(rootRule, 'Rule for /:path* should be defined');
  
  const headerMap = new Map();
  for (const h of rootRule.headers) {
    headerMap.set(h.key.toLowerCase(), h.value);
  }

  // Mandatory security headers
  assert.equal(headerMap.get('x-content-type-options'), 'nosniff');
  assert.equal(headerMap.get('x-frame-options'), 'DENY');
  assert.equal(headerMap.get('referrer-policy'), 'strict-origin-when-cross-origin');
  assert.ok(headerMap.has('permissions-policy'), 'permissions-policy must be defined');

  // CSP assertions
  const csp = headerMap.get('content-security-policy');
  assert.ok(csp, 'Content-Security-Policy must be defined');
  assert.ok(csp.includes("default-src 'self'"), 'CSP must include default-src self');
  assert.ok(!csp.includes("'unsafe-eval'"), 'Production/default CSP must NOT include unsafe-eval');
  assert.ok(csp.includes("connect-src 'self' ws: wss:"), 'CSP must allow ws and wss for WebSocket logs');
  assert.ok(csp.includes('http://127.0.0.1:4142'), 'CSP connect-src must permit Local Agent loopback');
  assert.ok(!/\bconnect-src[^;]*\shttps?:(?![/]{2})/.test(csp), 'CSP connect-src must NOT contain wildcard http: or https:');
  assert.ok(csp.includes("form-action 'self'"), 'CSP must restrict form-action to self');
  assert.ok(csp.includes("frame-ancestors 'none'"), 'CSP must prevent framing');
  assert.ok(csp.includes("object-src 'none'"), 'CSP must disable plugins/object-src');
});
