# Agent UI

The agent-facing app. Next.js 15 App Router, React 19, Tailwind 4,
NextAuth 5 (Zitadel OIDC).

## Layout

```
app/
├── layout.tsx                 root <html>/<body>
├── page.tsx                   /  -> redirect /inbox
├── api/auth/[...nextauth]/    NextAuth route handler
└── (app)/                     auth-guarded shell
    ├── layout.tsx             sidebar + sign-out
    ├── inbox/page.tsx         (filled in by next commit)
    └── supervisor/...         (filled in by next commit)
lib/
├── auth.ts                    NextAuth + Zitadel config (server-only)
└── api.ts                     Bearer-bearing fetch wrappers (server-only)
middleware.ts                  Route guard
```

## Routes

| Path | Auth | Purpose |
|---|---|---|
| `/api/auth/*` | public | NextAuth callback handlers |
| `/inbox` | required | ticket list (next commit) |
| `/tickets/:id` | required | ticket detail + composer (next commit) |
| `/supervisor` | required (supervisor role) | dashboard widgets (next commit) |

## Local dev

```sh
cp .env.local.example .env.local
# fill in ZITADEL_*, AUTH_SECRET
npm install
npm run dev
```

The dev server expects the Go gateway at `NEXT_PUBLIC_GATEWAY_URL`
(default `http://localhost:8080`). Sign-in goes via NextAuth ->
Zitadel; the access token NextAuth gets back is the same JWT the
gateway accepts on `/v1/*`.

## Performance posture

- Server components by default. Client components are opt-in (`"use
  client"`) and minimised.
- `lib/api.ts` uses the global fetch so per-render request dedupe is
  automatic.
- Static metadata + nav are module-scope (per `server-hoist-static-io`
  / `rerender-no-inline-components`).
- Heavy components (charts, file viewers) get loaded with `next/dynamic`
  in the next commit.

## Security headers

`next.config.ts` sets HSTS / CSP / Referrer-Policy / nosniff /
X-Frame-Options DENY / Permissions-Policy on every response. Mirrors
`internal/pkg/secheaders.Defaults()` so the gateway and the Next app
present the same posture even if a stale CDN intermediate strips one
layer.
