import { auth } from "@/lib/auth";
import { NextResponse } from "next/server";

// Route guard. Anything under /(app)/* requires a session; everything
// else (including /api/auth/*) is public.
//
// We use NextAuth's auth() inside middleware -- the v5 API supports
// being called directly. Returning NextResponse.next() lets the route
// continue; returning a redirect bounces unauthenticated users to the
// login page.
export default auth((req) => {
  const isAppRoute =
    req.nextUrl.pathname === "/" ||
    req.nextUrl.pathname.startsWith("/inbox") ||
    req.nextUrl.pathname.startsWith("/tickets") ||
    req.nextUrl.pathname.startsWith("/supervisor");

  if (!isAppRoute) {
    return NextResponse.next();
  }
  if (!req.auth) {
    const url = new URL("/api/auth/signin", req.nextUrl);
    url.searchParams.set("callbackUrl", req.nextUrl.pathname);
    return NextResponse.redirect(url);
  }
  return NextResponse.next();
});

// Skip Next.js internals + assets so the middleware doesn't run on
// every static fetch.
export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico|api/auth).*)"],
};
