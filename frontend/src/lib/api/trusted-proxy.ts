import net from 'net';

/**
 * Cleans and validates an IPv4 or IPv6 address.
 * Strips bracket wrappers and trailing port numbers if present.
 * Returns clean IP or null if invalid.
 */
export function cleanAndValidateIp(ip: string): string | null {
  if (!ip) return null;
  let cleanIp = ip.trim();
  if (cleanIp.startsWith('[') && cleanIp.includes(']')) {
    cleanIp = cleanIp.slice(1, cleanIp.indexOf(']'));
  } else if (cleanIp.includes(':') && !cleanIp.includes('::') && cleanIp.split(':').length === 2) {
    // IPv4 with port
    cleanIp = cleanIp.split(':')[0];
  }
  if (net.isIPv4(cleanIp) || net.isIPv6(cleanIp)) {
    return cleanIp;
  }
  return null;
}

/**
 * Checks whether an IP belongs to a configured list of trusted proxy IPs or CIDR blocks.
 */
export function isTrustedProxy(ip: string, trustedProxies: string[]): boolean {
  if (!ip || !trustedProxies || trustedProxies.length === 0) {
    return false;
  }

  const cleanIp = cleanAndValidateIp(ip);
  if (!cleanIp) {
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
 * Walks X-Forwarded-For from right to left, skipping configured trusted proxies.
 * Rejects client-supplied forwarded headers if the immediate peer connection is untrusted.
 * When direct peer IP is not available from the runtime (e.g. Next.js Web standard Request),
 * trusts the proxy chain if trustedProxies is configured with allowlisted reverse proxies.
 */
export function extractClientIp(
  headers: Headers,
  trustedProxies: string[],
  isDev: boolean,
  directPeerIp?: string
): string {
  // If an explicit directPeerIp is provided (e.g. in environments with direct socket access or tests),
  // verify whether that immediate connection is a trusted proxy.
  if (directPeerIp !== undefined && directPeerIp !== '') {
    const isDirectPeerTrusted = isTrustedProxy(directPeerIp, trustedProxies);
    if (!isDirectPeerTrusted) {
      // Direct peer is untrusted: reject all forwarded headers to prevent spoofing
      return cleanAndValidateIp(directPeerIp) || directPeerIp;
    }
  } else {
    // When directPeerIp is not provided (e.g. Next.js route handlers where request.ip and request.socket
    // are undocumented / unavailable), forwarding headers are only trusted if trusted reverse proxies
    // are explicitly configured or if running in local development mode.
    const canTrustForwarding = (trustedProxies && trustedProxies.length > 0) || isDev;
    if (!canTrustForwarding) {
      return '127.0.0.1';
    }
  }

  // Inspect X-Forwarded-For: traverse right-to-left
  const xff = headers.get('x-forwarded-for');
  if (xff) {
    const parts = xff.split(',').map((p) => p.trim()).filter(Boolean);
    // Walk right-to-left: the first non-trusted proxy IP is the authentic client IP
    for (let i = parts.length - 1; i >= 0; i--) {
      const candidate = cleanAndValidateIp(parts[i]);
      if (!candidate) {
        continue; // Skip malformed / invalid IP candidates
      }
      if (!isTrustedProxy(candidate, trustedProxies)) {
        return candidate;
      }
    }
    // If all candidates in chain were trusted proxies, return the leftmost valid IP
    for (const p of parts) {
      const candidate = cleanAndValidateIp(p);
      if (candidate) {
        return candidate;
      }
    }
  }

  // Fallback to X-Real-IP if X-Forwarded-For was absent or invalid
  const xri = headers.get('x-real-ip');
  if (xri) {
    const cleanXri = cleanAndValidateIp(xri);
    if (cleanXri && !isTrustedProxy(cleanXri, trustedProxies)) {
      return cleanXri;
    }
  }

  // Fallback to Cloudflare CF-Connecting-IP
  const cfIp = headers.get('cf-connecting-ip');
  if (cfIp) {
    const cleanCf = cleanAndValidateIp(cfIp);
    if (cleanCf && !isTrustedProxy(cleanCf, trustedProxies)) {
      return cleanCf;
    }
  }

  // Fallback to True-Client-IP
  const trueClientIp = headers.get('true-client-ip');
  if (trueClientIp) {
    const cleanTrue = cleanAndValidateIp(trueClientIp);
    if (cleanTrue && !isTrustedProxy(cleanTrue, trustedProxies)) {
      return cleanTrue;
    }
  }

  // Fallback to standard RFC 7239 Forwarded header: for=<ip>
  const forwarded = headers.get('forwarded');
  if (forwarded) {
    const match = /for="?([^;,"]+)"?/i.exec(forwarded);
    if (match && match[1]) {
      const forIp = cleanAndValidateIp(match[1]);
      if (forIp && !isTrustedProxy(forIp, trustedProxies)) {
        return forIp;
      }
    }
  }

  if (directPeerIp) {
    const cleanDirect = cleanAndValidateIp(directPeerIp);
    if (cleanDirect) return cleanDirect;
  }

  return '127.0.0.1';
}
