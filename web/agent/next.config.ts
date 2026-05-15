import type { NextConfig } from "next";

// Hardened Next.js config. The agent app is gated by Zitadel-issued
// JWTs and never serves anonymous traffic; we still set the same
// security headers the gateway sets so a stale CDN cache can't ship a
// reduced-CSP version.
const cspDefault =
  "default-src 'self'; " +
  "img-src 'self' data: blob:; " +
  "script-src 'self' 'unsafe-inline'; " + // unsafe-inline only for Next runtime; tighten later via nonce
  "style-src 'self' 'unsafe-inline'; " +
  "connect-src 'self' " +
    (process.env.NEXT_PUBLIC_GATEWAY_URL ?? "") + " " +
    (process.env.NEXT_PUBLIC_WS_URL ?? "") + "; " +
  "frame-ancestors 'none'; " +
  "base-uri 'none'; " +
  "form-action 'self'";

const config: NextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  // RSC + Server Actions stay default-on per Next 15.
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "Content-Security-Policy", value: cspDefault },
          { key: "Strict-Transport-Security", value: "max-age=31536000; includeSubDomains; preload" },
          { key: "Referrer-Policy", value: "no-referrer" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "X-Frame-Options", value: "DENY" },
          { key: "Permissions-Policy", value: "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()" },
        ],
      },
    ];
  },
};

export default config;
