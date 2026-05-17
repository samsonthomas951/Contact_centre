import { gatewayJSON, GatewayError } from "@/lib/api";
import { auth } from "@/lib/auth";
import { RegisterForm } from "./RegisterForm";
import { WidgetRow, type WidgetSite } from "./WidgetRow";

// Admin-only page for self-registering widget sites. List + register +
// remove. Reads from /v1/onboarding/widgets (admin-gated server-side);
// non-admins land here and see a "permission required" empty state
// rather than the gateway 500 leaking through.

export const dynamic = "force-dynamic";

export default async function WidgetsPage() {
  const session = await auth();
  if (!session) return null;

  // Compose the runtime WS URL exactly as the snippet should reference
  // it. Falls back to the host part of the public gateway URL if the
  // dedicated WS URL isn't set.
  const wsURL =
    process.env.NEXT_PUBLIC_WS_URL ??
    (process.env.NEXT_PUBLIC_GATEWAY_URL ?? "").replace(/^http/, "ws");

  let sites: WidgetSite[] = [];
  let error: string | null = null;
  try {
    const res = await gatewayJSON<{ widgets: WidgetSite[] }>(
      "/v1/onboarding/widgets",
      { cache: "no-store" },
    );
    sites = res.widgets ?? [];
  } catch (e) {
    if (e instanceof GatewayError && e.status === 403) {
      error = "Admin role required. Sign in as carol@demo.local to manage widgets.";
    } else {
      error = "Could not load widgets. Try refreshing.";
    }
  }

  return (
    <div className="max-w-3xl p-6">
      <header>
        <h1 className="text-2xl font-semibold">Widgets</h1>
        <p className="mt-1 text-sm text-slate-600">
          Each registered origin can run the embeddable chat widget.
          Visitor messages from any of these sites flow into the same
          agent inbox as Facebook DMs and WhatsApp messages.
        </p>
      </header>

      {error ? (
        <div className="mt-6 rounded border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
          {error}
        </div>
      ) : (
        <>
          <div className="mt-6">
            <RegisterForm />
          </div>

          <section className="mt-6 rounded border border-slate-200 bg-white">
            <header className="border-b border-slate-200 px-4 py-2 text-sm font-semibold">
              Registered sites ({sites.length})
            </header>
            {sites.length === 0 ? (
              <p className="px-4 py-6 text-sm text-slate-500">
                No sites registered yet. Use the form above to register one.
              </p>
            ) : (
              <ul>
                {sites.map((s) => (
                  <WidgetRow key={s.id} site={s} wsURL={wsURL} />
                ))}
              </ul>
            )}
          </section>
        </>
      )}
    </div>
  );
}
