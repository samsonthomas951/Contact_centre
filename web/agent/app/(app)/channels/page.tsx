import { gatewayJSON, GatewayError } from "@/lib/api";
import { auth } from "@/lib/auth";

// Channels admin page. One "Connect Facebook" button covers all three
// Meta channels — the OAuth callback also discovers any IG Business
// Accounts linked to the connected Pages and any WhatsApp Business
// Accounts owned by the user's Businesses, then auto-subscribes their
// webhooks. Lists all three in their own sections below.

export const dynamic = "force-dynamic";

interface FBPage {
  page_id: string;
  page_name: string;
  webhook_subscribed: boolean;
  created_at: string;
}
interface IGAccount {
  ig_user_id: string;
  username: string;
  fb_page_id: string;
  webhook_subscribed: boolean;
}
interface WAPhone {
  phone_number_id: string;
  waba_id: string;
  display_phone_number: string;
  webhook_subscribed: boolean;
}

async function fetchOrEmpty<T>(path: string, key: string): Promise<T[]> {
  try {
    const res = await gatewayJSON<Record<string, T[]>>(path, { cache: "no-store" });
    return res[key] ?? [];
  } catch (e) {
    if (!(e instanceof GatewayError && e.status === 404)) {
      // eslint-disable-next-line no-console
      console.warn(`${path} failed:`, e);
    }
    return [];
  }
}

export default async function ChannelsPage() {
  const session = await auth();
  if (!session) return null;

  // Three independent fetches in parallel.
  const [pages, ig, wa] = await Promise.all([
    fetchOrEmpty<FBPage>("/v1/connect/fb/pages", "pages"),
    fetchOrEmpty<IGAccount>("/v1/connect/fb/ig", "ig"),
    fetchOrEmpty<WAPhone>("/v1/connect/fb/wa", "wa"),
  ]);

  const gatewayURL = process.env.NEXT_PUBLIC_GATEWAY_URL ?? "";
  const tenantID = "11111111-1111-1111-1111-111111111111"; // demo
  const connectURL = `${gatewayURL}/v1/connect/fb?tenant=${tenantID}`;

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-2xl font-semibold">Channels</h1>
      <p className="mt-2 text-sm text-slate-600">
        One Facebook login connects all three Meta channels — Pages,
        Instagram Business accounts linked to those Pages, and WhatsApp
        Business numbers owned by your Business. Each channel's webhook
        subscription is automatic.
      </p>

      <div className="mt-6 flex justify-end">
        <a
          href={connectURL}
          className="rounded bg-brand px-4 py-2 text-sm font-semibold text-white hover:bg-brand-dark"
        >
          Connect with Facebook
        </a>
      </div>

      <Section
        title="Facebook Pages"
        empty="No Pages connected yet."
        rows={pages.map((p) => ({
          primary: p.page_name,
          secondary: `ID ${p.page_id}`,
          subscribed: p.webhook_subscribed,
        }))}
      />

      <Section
        title="Instagram accounts"
        empty="No Instagram Business accounts found on the connected Pages."
        rows={ig.map((i) => ({
          primary: `@${i.username}`,
          secondary: `IG user ${i.ig_user_id} · linked to Page ${i.fb_page_id}`,
          subscribed: i.webhook_subscribed,
        }))}
      />

      <Section
        title="WhatsApp numbers"
        empty="No WhatsApp Business phone numbers found on your Business Accounts."
        rows={wa.map((w) => ({
          primary: w.display_phone_number,
          secondary: `phone ${w.phone_number_id} · WABA ${w.waba_id}`,
          subscribed: w.webhook_subscribed,
        }))}
      />

      <section className="mt-8 rounded border border-slate-200 bg-amber-50 p-4 text-sm text-amber-900">
        <p className="font-semibold">Demo mode</p>
        <p className="mt-1">
          Set <code>FB_APP_ID</code>, <code>FB_APP_SECRET</code>, and{" "}
          <code>FB_REDIRECT_URI</code> on the gateway, then point your
          Meta Developer App's webhook URLs at:
        </p>
        <ul className="mt-1 ml-6 list-disc">
          <li><code>{gatewayURL}/v1/fb/webhook</code> for Messenger</li>
          <li><code>{gatewayURL}/v1/ig/webhook</code> for Instagram</li>
          <li><code>{gatewayURL}/v1/wa/webhook</code> for WhatsApp</li>
        </ul>
        <p className="mt-1">
          Verify token: your <code>FB_APP_SECRET</code>. Full setup in{" "}
          <code>docs/demo/README.md</code>.
        </p>
      </section>
    </div>
  );
}

// Module-scope per rerender-no-inline-components.
type Row = { primary: string; secondary: string; subscribed: boolean };
function Section({ title, empty, rows }: { title: string; empty: string; rows: Row[] }) {
  return (
    <section className="mt-6 rounded border border-slate-200 bg-white">
      <header className="border-b border-slate-200 px-4 py-2 text-sm font-semibold">
        {title} ({rows.length})
      </header>
      <div className="px-4 py-3">
        {rows.length === 0 ? (
          <p className="text-sm text-slate-500">{empty}</p>
        ) : (
          <ul className="divide-y divide-slate-100">
            {rows.map((r, i) => (
              <li key={r.primary + i} className="flex items-center justify-between py-2">
                <div>
                  <div className="text-sm font-medium">{r.primary}</div>
                  <div className="text-xs text-slate-500">{r.secondary}</div>
                </div>
                <span className={"text-xs " + (r.subscribed ? "text-emerald-700" : "text-slate-400")}>
                  {r.subscribed ? "subscribed ✓" : "not subscribed"}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
