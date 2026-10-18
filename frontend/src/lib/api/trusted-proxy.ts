import net from 'net';

/**
 * Checks whether an IP belongs to a configured list of trusted proxy IPs or CIDR blocks.
 */
export function isTrustedProxy(ip: string, trustedProxies: string[]): boolean {
  if (!ip || !trustedProxies || trustedProxies.length === 0) {
    return false;
  }

  // Strip port if present (e.g. "127.0.0.1:34567" or "[::1]:34567")
  let cleanIp = ip.trim();
  if (cleanIp.startsWith('[') && cleanIp.includes(']')) {
    cleanIp = cleanIp.slice(1, cleanIp.indexOf(']'));
  } else if (cleanIp.includes(':') && !cleanIp.includes('::') && cleanIp.split(':').length === 2) {
    // IPv4 with port
    cleanIp = cleanIp.split(':')[0];
  }

  const isV4 = net.isIPv4(cleanIp);
  const isV6 = net.isIPv6(cleanIp);
  if (!isV4 && !isV6) {
    return false;
  }

  for (const proxy of trustedProxies) {
    const trimmed = proxy.trim();
    if (!trimmed) continue;

    if (trimmed.includes('/')) {
      if (ipInCidr(cleanIp, trimmed)) {
        return true;
      }
    } else {
      if (cleanIp === trimmed) {
        return true;
      }
      // Handle localhost aliases
      if (
        (trimmed === 'localhost' || trimmed === '127.0.0.1' || trimmed === '::1') &&
        (cleanIp === '127.0.0.1' || cleanIp === '::1' || cleanIp === '::ffff:127.0.0.1')
      ) {
        return true;
      }
    }
  }

  return false;
}

/**
 * Checks whether an IPv4 address is within a CIDR range.
 */
function ipInCidr(ip: string, cidr: string): boolean {
  const [range, bitsStr] = cidr.split('/');
  const bits = parseInt(bitsStr, 10);
  if (isNaN(bits) || bits < 0 || bits > 32) {
    return false;
  }

  // Only handle IPv4 CIDRs here
  if (!net.isIPv4(ip) || !net.isIPv4(range)) {
    return false;
  }

  const ipNum = ipv4ToNumber(ip);
  const rangeNum = ipv4ToNumber(range);
  const mask = bits === 0 ? 0 : (~0 << (32 - bits)) >>> 0;

  return (ipNum & mask) === (rangeNum & mask);
}

function ipv4ToNumber(ip: string): number {
  return (
    ip
      .split('.')
      .reduce((acc, octet) => (acc << 8) + parseInt(octet, 10), 0) >>> 0
  );
}

/**
 * Extracts authentic client IP from request headers according to a trusted-proxy model.
 * Walks X-Forwarded-For from right to left, skipping trusted proxies.
 * Rejects client-supplied forwarded headers if the immediate connection is untrusted.
 */
export function extractClientIp(
  headers: Headers,
  trustedProxies: string[],
  isDev: boolean,
  directPeerIp?: string
): string {
  const immediatePeer = directPeerIp || (isDev ? '127.0.0.1' : '');

  // If no trusted proxies are configured or immediate peer is untrusted,
  // do NOT trust any forwarding headers (they are untrusted / client-spoofed)
  const isPeerTrusted = immediatePeer ? isTrustedProxy(immediatePeer, trustedProxies) : isDev;

  if (isPeerTrusted) {
    const xff = headers.get('x-forwarded-for');
    if (xff) {
      const parts = xff.split(',').map((p) => p.trim()).filter(Boolean);
      // Walk right-to-left: the first non-trusted proxy is the authentic client IP
      for (let i = parts.length - 1; i >= 0; i--) {
        const candidate = parts[i];
        if (!isTrustedProxy(candidate, trustedProxies)) {
          return candidate;
        }
      }
      // If all are trusted proxies, the leftmost was the first hop
      if (parts.length > 0) {
        return parts[0];
      }
    }

    const xri = headers.get('x-real-ip');
    if (xri && xri.trim()) {
      const cleanXri = xri.trim();
      if (!isTrustedProxy(cleanXri, trustedProxies)) {
        return cleanXri;
      }
    }

    const cfIp = headers.get('cf-connecting-ip');
    if (cfIp && cfIp.trim()) {
      return cfIp.trim();
    }
  }

  // Fallback to immediate peer or loopback
  return immediatePeer || '127.0.0.1';
}
