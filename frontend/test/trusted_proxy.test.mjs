import test from 'node:test';
import assert from 'node:assert/strict';
import { isTrustedProxy, extractClientIp } from '../src/lib/api/trusted-proxy.ts';

test('isTrustedProxy identifies IP addresses and CIDR blocks correctly', () => {
  const proxies = ['127.0.0.1', '::1', '10.0.0.0/8', '172.16.0.0/12', '192.168.1.50'];

  // Exact IPv4 matches
  assert.equal(isTrustedProxy('127.0.0.1', proxies), true);
  assert.equal(isTrustedProxy('127.0.0.1:8080', proxies), true);
  assert.equal(isTrustedProxy('192.168.1.50', proxies), true);
  assert.equal(isTrustedProxy('192.168.1.51', proxies), false);

  // Exact IPv6 matches
  assert.equal(isTrustedProxy('::1', proxies), true);
  assert.equal(isTrustedProxy('[::1]:3000', proxies), true);
  assert.equal(isTrustedProxy('2001:db8::1', proxies), false);

  // CIDR matches
  assert.equal(isTrustedProxy('10.1.2.3', proxies), true);
  assert.equal(isTrustedProxy('172.18.0.4', proxies), true);
  assert.equal(isTrustedProxy('172.31.255.255', proxies), true);
  assert.equal(isTrustedProxy('172.32.0.1', proxies), false);
  assert.equal(isTrustedProxy('203.0.113.195', proxies), false);

  // Empty or invalid
  assert.equal(isTrustedProxy('', proxies), false);
  assert.equal(isTrustedProxy('invalid-ip', proxies), false);
  assert.equal(isTrustedProxy('127.0.0.1', []), false);
});

test('extractClientIp rejects spoofed headers from untrusted direct peer', () => {
  const trustedProxies = ['10.0.0.1', '172.18.0.1'];
  const headers = new Headers();
  headers.set('x-forwarded-for', '8.8.8.8');
  headers.set('x-real-ip', '1.1.1.1');

  // Untrusted peer
  const clientIp = extractClientIp(headers, trustedProxies, false, '203.0.113.50');
  assert.equal(clientIp, '203.0.113.50');
});

test('extractClientIp walks right-to-left skipping trusted proxies', () => {
  const trustedProxies = ['10.0.0.1', '172.18.0.0/16', '127.0.0.1'];

  // Attacker spoofed 1.1.1.1, real client is 198.51.100.42, proxy is 10.0.0.1
  const headers = new Headers();
  headers.set('x-forwarded-for', '1.1.1.1, 198.51.100.42, 10.0.0.1');

  const clientIp = extractClientIp(headers, trustedProxies, false, '172.18.0.2');
  assert.equal(clientIp, '198.51.100.42');
});

test('extractClientIp falls back to x-real-ip if x-forwarded-for absent', () => {
  const trustedProxies = ['127.0.0.1'];
  const headers = new Headers();
  headers.set('x-real-ip', '198.51.100.99');

  const clientIp = extractClientIp(headers, trustedProxies, false, '127.0.0.1');
  assert.equal(clientIp, '198.51.100.99');
});

test('extractClientIp defaults in development mode', () => {
  const headers = new Headers();
  headers.set('x-forwarded-for', '198.51.100.25');

  // Dev mode trusts local requests
  const clientIp = extractClientIp(headers, ['127.0.0.1'], true, '127.0.0.1');
  assert.equal(clientIp, '198.51.100.25');
});
