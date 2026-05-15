import { gatewayJSON, GatewayError } from "@/lib/api";
import { auth } from "@/lib/auth";

// Channels admin page. Shows the Pages connected to this tenant and a
// "Connect Facebook" button that kicks off the OAuth flow on the
// gateway.
//
// The button is just a link to /v1/connect/fb?tenant=<id> on the
// gateway. The gateway redirects to Meta's consent screen, then back
// to /v1/connect/fb/callback, which subscribes the chosen Pages and
// returns a friendly HTML page.

export const dynamic = "force-dynamic";

interface FBPage {
  page_id: string;
  page_name: string;
  webhook_subscribed: boolean;
  created_at: string;
}

export default async function ChannelsPage() {
  const session = await auth();
  if (!session) return null; // middleware redirects; defensive

  // Surface what the tenant has connected. We add a tiny endpoint
  // (GET /v1/connect/fb/pages) below; for now we hit it best-effort
  // and tolerate a 404 if the gateway doesn't expose it yet.
  let pages: FBPage[] = [];
  try {
    const res = await gatewayJSON<{ pages: FBPage[] }>("/v1/connect/fb/pages",
      { cache: "no-store" });
    pages = res.pages ?? [];
  } catch (e) {
    // Tolerate the endpoint being absent; UI shows "no Pages".
    if (!(e instanceof GatewayError && e.status === 404)) {
      // eslint-disable-next-line no-console
      console.warn("connected pages list failed:", e);
    }
  }

  // The OAuth `state` carries the tenant ID. The gateway also accepts
  // a `tenant=` query param so the demo can hand it directly without
  // needing a full session-cookie wiring.
  const gatewayURL = process.env.NEXT_PUBLIC_GATEWAY_URL ?? "";
  // Hard-coded demo tenant since this is the demo flow; production
  // pulls it from the agent's session.
  const tenantID = "11111111-1111-1111-1111-111111111111";
  const connectURL = `${gatewayURL}/v1/connect/fb?tenant=${tenantID}`;

  return (
    <div className="p-6">
      <h1 className="text-2xl font-semibold">Channels</h1>
      <p className="mt-2 text-sm text-slate-600">
        Connect a social media account so customer messages flow into the
        inbox. Each connection is per-tenant; webhook subscription is
        automatic.
      </p>

      <section className="mt-8 rounded border border-slate-200 bg-white">
        <header className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h2 className="font-semibold">Facebook Pages</h2>
          <a
            href={connectURL}
            className="rounded bg-brand px-3 py-1.5 text-sm font-semibold text-white hover:bg-brand-dark"
          >
            Connect Facebook
          </a>
        </header>
        <div className="px-4 py-3">
          {pages.length === 0 ? (
            <p className="text-sm text-slate-500">
              No Pages connected yet. Click <em>Connect Facebook</em> above
              and pick the Pages you want messages from.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {pages.map((p) => (
                <li key={p.page_id} className="flex items-center justify-between py-2">
                  <div>
                    <div className="text-sm font-medium">{p.page_name}</div>
                    <div className="text-xs text-slate-500">
                      ID {p.page_id} · subscribed {p.webhook_subscribed ? "✓" : "✗"}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      <section className="mt-8 rounded border border-slate-200 bg-amber-50 p-4 text-sm text-amber-900">
        <p className="font-semibold">Demo mode</p>
        <p className="mt-1">
          Set <code>FB_APP_ID</code>, <code>FB_APP_SECRET</code>, and{" "}
          <code>FB_REDIRECT_URI</code> on the gateway, then point your
          Meta Developer App's webhook URL at{" "}
          <code>{gatewayURL}/v1/fb/webhook</code> with the verify token
          set to your <code>FB_APP_SECRET</code>. See{" "}
          <code>docs/demo/README.md</code> for the full setup.
        </p>
      </section>
    </div>
  );
}
