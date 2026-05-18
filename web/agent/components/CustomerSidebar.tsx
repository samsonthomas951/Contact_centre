import Link from "next/link";
import { gatewayJSON } from "@/lib/api";
import { formatRelative } from "@/lib/format";

// Customer profile rendered alongside the message thread on the
// ticket detail page. Pure server component; one request to the
// gateway's /v1/customers/by-ticket/{id}. The page wraps this in
// Suspense so a slow customer fetch doesn't block the thread.

interface Profile {
  id: string;
  display_name: string;
  email?: string;
  phone?: string;
  channels: { channel: string; external_ref: string }[];
  ticket_count: number;
  first_contact_at?: string;
  last_contact_at?: string;
  recent_tickets: {
    id: string;
    state: string;
    channel: string;
    subject: string;
    created_at: string;
  }[];
}

export async function CustomerSidebar({ ticketId }: { ticketId: string }) {
  let profile: Profile | null = null;
  try {
    profile = await gatewayJSON<Profile>(
      `/v1/customers/by-ticket/${encodeURIComponent(ticketId)}`,
      { cache: "no-store" },
    );
  } catch {
    // Swallow -- the main ticket view stays usable even if the
    // sidebar can't load. We render a tiny placeholder instead.
    return (
      <aside className="border-l border-slate-200 bg-slate-50 p-4 text-xs text-slate-500">
        Couldn&apos;t load customer info.
      </aside>
    );
  }

  return (
    <aside className="w-72 shrink-0 overflow-y-auto border-l border-slate-200 bg-slate-50 p-4 text-sm">
      <h2 className="text-base font-semibold text-slate-900">{profile.display_name || "Unknown"}</h2>
      {profile.email && <div className="mt-0.5 truncate text-xs text-slate-600">{profile.email}</div>}
      {profile.phone && <div className="text-xs text-slate-600">{profile.phone}</div>}

      <Section title="Channels">
        {profile.channels.length === 0 ? (
          <p className="text-xs text-slate-500">No channels recorded.</p>
        ) : (
          <ul className="space-y-1">
            {profile.channels.map((c) => (
              <li key={c.channel} className="flex items-center justify-between gap-2 text-xs">
                <span className="rounded bg-slate-200 px-1.5 py-0.5 uppercase tracking-wide text-slate-700">
                  {c.channel}
                </span>
                <span className="truncate font-mono text-[11px] text-slate-600" title={c.external_ref}>
                  {c.external_ref}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="History">
        <dl className="grid grid-cols-2 gap-x-2 gap-y-1 text-xs">
          <dt className="text-slate-500">Total tickets</dt>
          <dd className="text-right font-medium text-slate-800">{profile.ticket_count}</dd>
          {profile.first_contact_at && (
            <>
              <dt className="text-slate-500">First contact</dt>
              <dd className="text-right text-slate-700">{formatRelative(profile.first_contact_at)}</dd>
            </>
          )}
          {profile.last_contact_at && (
            <>
              <dt className="text-slate-500">Last contact</dt>
              <dd className="text-right text-slate-700">{formatRelative(profile.last_contact_at)}</dd>
            </>
          )}
        </dl>
      </Section>

      {profile.recent_tickets.length > 1 && (
        <Section title="Recent tickets">
          <ul className="space-y-1.5">
            {profile.recent_tickets
              .filter((t) => t.id !== ticketId)
              .slice(0, 6)
              .map((t) => (
                <li key={t.id}>
                  <Link
                    href={`/tickets/${t.id}`}
                    prefetch={false}
                    className="block rounded border border-slate-200 bg-white px-2 py-1.5 text-xs hover:bg-slate-100"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="rounded bg-slate-100 px-1 py-0.5 text-[10px] uppercase text-slate-600">
                        {t.channel}
                      </span>
                      <span className="text-[10px] text-slate-500">{t.state}</span>
                      <span className="ml-auto text-[10px] text-slate-400">
                        {formatRelative(t.created_at)}
                      </span>
                    </div>
                    <div className="mt-0.5 truncate text-slate-700">{t.subject || "(no subject)"}</div>
                  </Link>
                </li>
              ))}
          </ul>
        </Section>
      )}
    </aside>
  );
}

// Module-scope per rerender-no-inline-components.
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-4 border-t border-slate-200 pt-3">
      <h3 className="mb-2 text-[11px] font-semibold uppercase tracking-wide text-slate-500">{title}</h3>
      {children}
    </section>
  );
}
