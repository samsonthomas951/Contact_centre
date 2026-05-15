import Link from "next/link";
import { gatewayJSON, GatewayError } from "@/lib/api";
import type { Ticket } from "@/lib/types";
import { formatRelative, priorityLabel } from "@/lib/format";

// Server component. The inbox renders the ticket list at request time
// with `cache: 'no-store'` -- ticket state changes constantly and the
// page never wants a stale read. The realtime push that arrives via
// /ws/agent triggers a router.refresh() in the client subtree (see
// components/RealtimeRefresher.tsx).
export const dynamic = "force-dynamic";

export default async function InboxPage() {
  let tickets: Ticket[];
  try {
    const res = await gatewayJSON<{ tickets: Ticket[] }>(
      "/v1/tickets",
      { cache: "no-store" },
    );
    tickets = res.tickets ?? [];
  } catch (e) {
    if (e instanceof GatewayError && e.status === 401) {
      // The middleware should have caught this, but if the access
      // token expired between page nav and fetch we surface a clean
      // sign-in prompt instead of a stack trace.
      return <div className="p-6 text-sm text-slate-600">Session expired. Reload to sign in again.</div>;
    }
    throw e;
  }

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-center justify-between border-b border-slate-200 bg-white px-6 py-3">
        <h1 className="text-lg font-semibold">Inbox</h1>
        <span className="text-sm text-slate-500">{tickets.length} open</span>
      </header>
      <ul className="flex-1 overflow-auto bg-white">
        {tickets.length === 0 && (
          <li className="p-6 text-sm text-slate-500">
            Nothing in your inbox right now.
          </li>
        )}
        {tickets.map((t) => (
          <TicketRow key={t.id} t={t} />
        ))}
      </ul>
    </div>
  );
}

// Module-scope per rerender-no-inline-components.
function TicketRow({ t }: { t: Ticket }) {
  return (
    <li className="border-b border-slate-100">
      <Link
        href={`/tickets/${t.id}`}
        className="flex items-center gap-3 px-6 py-3 hover:bg-slate-50"
        prefetch={false}
      >
        <div className={`h-2 w-2 rounded-full ${stateDot(t.state)}`} />
        <div className="flex-1 truncate">
          <div className="truncate text-sm font-medium">{t.id.slice(0, 8)}</div>
          <div className="truncate text-xs text-slate-500">
            {priorityLabel(t.priority)} · {t.state}
          </div>
        </div>
        <div className="text-xs text-slate-500">
          {formatRelative(t.created_at)}
        </div>
      </Link>
    </li>
  );
}

function stateDot(s: string): string {
  switch (s) {
    case "new": return "bg-blue-500";
    case "open": return "bg-amber-500";
    case "pending": return "bg-purple-500";
    case "resolved":
    case "closed": return "bg-slate-300";
    default: return "bg-slate-400";
  }
}
