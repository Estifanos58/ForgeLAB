import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';

/**
 * Next.js 16 Proxy convention for request interception at the network boundary.
 * Replaces the deprecated middleware.ts convention.
 */
export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  const accessToken = request.cookies.get('forgelab_access_token');
  const refreshToken = request.cookies.get('forgelab_refresh_token');
  const hasSession = Boolean(accessToken?.value || refreshToken?.value);

  // Protected application routes
  const isProtectedRoute = pathname.startsWith('/dashboard') || pathname.startsWith('/projects');

  // Authentication entry routes
  const isAuthRoute = pathname === '/login' || pathname === '/register';

  if (isProtectedRoute && !hasSession) {
    const loginUrl = new URL('/login', request.url);
    loginUrl.searchParams.set('from', pathname);
    return NextResponse.redirect(loginUrl);
  }

  if (isAuthRoute && hasSession && accessToken?.value) {
    return NextResponse.redirect(new URL('/dashboard', request.url));
  }

  return NextResponse.next();
}

export const config = {
  matcher: ['/dashboard/:path*', '/projects/:path*', '/login', '/register'],
};
