import { NextRequest, NextResponse } from 'next/server';

export const dynamic = 'force-dynamic';

function getBackendBaseUrl(): string {
  return process.env.BACKEND_INTERNAL_URL || process.env.BACKEND_URL || 'http://localhost:8080';
}

async function handleProxy(request: NextRequest) {
  const backendBase = getBackendBaseUrl();
  const pathname = request.nextUrl.pathname;
  const search = request.nextUrl.search;
  const targetUrl = `${backendBase}${pathname}${search}`;

  // Forward incoming headers, excluding hop-by-hop headers and untrusted client forwarding headers
  const forwardedHeaders = new Headers();
  request.headers.forEach((value, key) => {
    const lowerKey = key.toLowerCase();
    if (
      lowerKey === 'host' ||
      lowerKey === 'connection' ||
      lowerKey === 'keep-alive' ||
      lowerKey === 'transfer-encoding' ||
      lowerKey === 'upgrade' ||
      lowerKey.startsWith('x-forwarded-') ||
      lowerKey === 'x-real-ip' ||
      lowerKey === 'forwarded'
    ) {
      return; // Strip hop-by-hop and client-supplied forwarding headers
    }
    forwardedHeaders.set(key, value);
  });

  // Explicitly ensure Cookie and Authorization are preserved
  const cookieHeader = request.headers.get('cookie');
  if (cookieHeader) {
    forwardedHeaders.set('cookie', cookieHeader);
  }
  const authHeader = request.headers.get('authorization');
  if (authHeader) {
    forwardedHeaders.set('authorization', authHeader);
  }

  // Derive actual client IP from trusted request environment (not untrusted client header)
  const clientIp = (request as any).ip || (request as any).socket?.remoteAddress || '127.0.0.1';
  const proto = request.nextUrl.protocol.replace(':', '') || 'http';
  const host = request.headers.get('host') || 'localhost:3000';

  forwardedHeaders.set('x-forwarded-for', clientIp);
  forwardedHeaders.set('x-forwarded-proto', proto);
  forwardedHeaders.set('x-forwarded-host', host);
  forwardedHeaders.set('forwarded', `for=${clientIp};proto=${proto};host=${host}`);

  // Handle body
  const isBodyAllowed = request.method !== 'GET' && request.method !== 'HEAD';
  const body = isBodyAllowed ? request.body : undefined;

  const fetchOptions: RequestInit & { duplex?: 'half' } = {
    method: request.method,
    headers: forwardedHeaders,
    body,
    redirect: 'manual', // Preserve 3xx redirects without internally following
  };

  if (body) {
    fetchOptions.duplex = 'half';
  }

  let backendResponse: Response;
  try {
    backendResponse = await fetch(targetUrl, fetchOptions);
  } catch (err: any) {
    return NextResponse.json(
      { error: `Backend service unavailable: ${err?.message || 'connection failed'}` },
      { status: 502 }
    );
  }

  // Build response headers
  const responseHeaders = new Headers();
  backendResponse.headers.forEach((value, key) => {
    const lower = key.toLowerCase();
    // Do not pass set-cookie here; handle it via getSetCookie() to prevent comma concatenation
    if (lower !== 'set-cookie' && lower !== 'content-encoding' && lower !== 'content-length') {
      responseHeaders.set(key, value);
    }
  });

  // Explicitly preserve backend Location header
  const location = backendResponse.headers.get('location');
  if (location) {
    responseHeaders.set('location', location);
  }

  // Explicitly preserve backend Set-Cookie headers
  const getSetCookie = (backendResponse.headers as any).getSetCookie;
  if (typeof getSetCookie === 'function') {
    const cookies: string[] = getSetCookie.call(backendResponse.headers);
    for (const c of cookies) {
      responseHeaders.append('set-cookie', c);
    }
  } else {
    const rawSetCookie = backendResponse.headers.get('set-cookie');
    if (rawSetCookie) {
      responseHeaders.set('set-cookie', rawSetCookie);
    }
  }

  return new NextResponse(backendResponse.body, {
    status: backendResponse.status,
    statusText: backendResponse.statusText,
    headers: responseHeaders,
  });
}

export const GET = handleProxy;
export const POST = handleProxy;
export const PUT = handleProxy;
export const PATCH = handleProxy;
export const DELETE = handleProxy;
export const OPTIONS = handleProxy;
export const HEAD = handleProxy;
