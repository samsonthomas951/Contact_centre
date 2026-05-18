import Link from "next/link";
import { gatewayJSON, GatewayError } from "@/lib/api";
import { formatRelative, priorityLabel } from "@/lib/format";
import { FilterBar } from "./FilterBar";
import { InboxShortcuts } from "@/components/InboxShortcuts";
import { InboxBulkBar, type TeamMember } from "@/components/InboxBulkBar";
import { TagChip } from "@/components/TagChip";
import { auth } from "@/lib/auth";
import type { Tag } from "@/lib/types";

// Server component. The inbox renders the ticket list at request time
// with `cache: 'no-store'` -- ticket state changes constantly and the
// page never wants a stale read. The realtime push that arrives via
// /ws/agent triggers a router.refresh() in the client subtree (see
// components/RealtimeRefresher.tsx).
//
// Filter state lives in the URL search params so it's bookmarkable
// and survives a /ws/agent refresh; FilterBar mutates the URL, this
// page re-runs with the new params on every push.

export const dynamic = "force-dynamic";

interface ListItem {
  id: string;
  state: string;
  priority: number;
  channel: string;
  customer_name: string;
  last_message_body?: string;
  message_count: number;
  created_at: string;
  assigned_agent_id?: string | null;
  tags?: Tag[];
}

export default async function InboxPage({
  searchParams,
}: {
  // Next 15 hands us a Promise; await before reading.
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const sp = await searchParams;
  const path = buildPath(sp);

  let tickets: ListItem[] = [];
  let tags: Tag[] = [];
  let team: TeamMember[] = [];
  // Pull the caller's identity for the BulkBar so a plain agent can
  // still self-assign even when the team listing is gated to
  // supervisors+.
  const session = await auth();
  const tok = session?.accessToken ?? "";
  const isDemo = tok.startsWith("demo:");
  const meId = isDemo ? tok.split(":")[1] ?? "" : "";
  const meRole = isDemo ? tok.split(":")[3] ?? "" : "";
  const meEmail = session?.user?.email ?? "me";

  try {
    // Tags + team list fetched in parallel with tickets so the
    // FilterBar + BulkBar have everything they need at first paint.
    const [tk, tg, ag] = await Promise.all([
      gatewayJSON<{ tickets: ListItem[] }>(path, { cache: "no-store" }),
      gatewayJSON<{ tags: Tag[] }>("/v1/tags/", { cache: "no-store" }).catch(
        () => ({ tags: [] as Tag[] }),
      ),
      gatewayJSON<{ agents: { agent_id: string; display_name: string }[] }>(
        "/v1/supervisor/agents",
        { cache: "no-store" },
      ).catch(() => ({ agents: [] as { agent_id: string; display_name: string }[] })),
    ]);
    tickets = tk.tickets ?? [];
    tags = tg.tags ?? [];
    team = (ag.agents ?? []).map((a) => ({ id: a.agent_id, name: a.display_name }));
  } catch (e) {
    if (e instanceof GatewayError && e.status === 401) {
      return <div className="p-6 text-sm text-slate-600">Session expired. Reload to sign in again.</div>;
    }
    throw e;
  }

  // Plain agents can't read /supervisor/agents. Fall back to a
  // self-only roster so the assign dropdown still shows "Me".
  if (team.length === 0 && meId) {
    team = [{ id: meId, name: meEmail }];
  }
  const canAssignOthers =
    meRole.split(",").includes("supervisor") || meRole.split(",").includes("admin");

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-center justify-between border-b border-slate-200 bg-white px-6 py-3">
        <div className="flex items-baseline">
          <h1 className="text-lg font-semibold">Inbox</h1>
          <InboxShortcuts ticketIds={tickets.map((t) => t.id)} />
        </div>
        <span className="text-sm text-slate-500">{tickets.length} tickets</span>
      </header>
      <FilterBar tags={tags} />
      <ul className="flex-1 overflow-auto bg-white pb-20">
        {tickets.length === 0 ? (
          <li className="p-6 text-sm text-slate-500">
            No tickets match these filters.
          </li>
        ) : (
          tickets.map((t) => <TicketRow key={t.id} t={t} />)
        )}
      </ul>
      <InboxBulkBar
        tags={tags}
        team={team}
        meId={meId}
        canAssignOthers={canAssignOthers}
      />
    </div>
  );
}

// Build the gateway path from the parsed search params. Arrays
// (`channel`, `state`) repeat the key. We deliberately don't pass
// any param the gateway doesn't recognise -- keeps the URL clean.
function buildPath(sp: Record<string, string | string[] | undefined>): string {
  const q = new URLSearchParams();
  for (const key of ["q", "assigned", "limit"] as const) {
    const v = sp[key];
    if (typeof v === "string" && v !== "") q.set(key, v);
  }
  for (const key of ["channel", "state", "tag"] as const) {
    const v = sp[key];
    if (Array.isArray(v)) for (const item of v) q.append(key, item);
    else if (typeof v === "string" && v !== "") q.append(key, v);
  }
  const qs = q.toString();
  return qs ? `/v1/tickets?${qs}` : "/v1/tickets";
}

// Module-scope per rerender-no-inline-components.
function TicketRow({ t }: { t: ListItem }) {
  const preview = (t.last_message_body ?? "").replace(/\s+/g, " ").trim();
  return (
    <li className="flex items-center border-b border-slate-100">
      {/* Plain HTML checkbox; the InboxBulkBar listens at the
          window level for change events and keeps the selection
          set itself, so no per-row React state. */}
      <label className="flex shrink-0 items-center pl-6 pr-2 py-3">
        <input
          type="checkbox"
          data-ticket-select
          value={t.id}
          className="h-4 w-4 rounded border-slate-300"
        />
      </label>
      <Link
        href={`/tickets/${t.id}`}
        // data-ticket-id lets the InboxShortcuts client component
        // find this row by id so it can focus/scroll it.
        data-ticket-id={t.id}
        className="flex flex-1 items-center gap-3 pr-6 py-3 hover:bg-slate-50 focus:bg-slate-100 focus:outline-none"
        prefetch={false}
      >
        <div className={`h-2 w-2 shrink-0 rounded-full ${stateDot(t.state)}`} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2 text-sm">
            <span className="font-medium text-slate-900">{t.customer_name || t.id.slice(0, 8)}</span>
            <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-600">
              {t.channel}
            </span>
            <span className="text-[10px] text-slate-400">{priorityLabel(t.priority)}</span>
            {!t.assigned_agent_id && (
              <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-800">unassigned</span>
            )}
          </div>
          <div className="truncate text-xs text-slate-500">{preview || "(no messages)"}</div>
          {t.tags && t.tags.length > 0 && (
            <div className="mt-1 flex flex-wrap gap-1">
              {t.tags.map((tag) => (
                <TagChip key={tag.id} tag={tag} />
              ))}
            </div>
          )}
        </div>
        <div className="shrink-0 text-right text-xs text-slate-500">
          <div>{formatRelative(t.created_at)}</div>
          <div className="text-[10px] text-slate-400">{t.message_count} msg</div>
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
