import { Suspense } from "react";
import { gatewayJSON } from "@/lib/api";
import type { QueueDepth, AgentPresence, AtRiskTicket } from "@/lib/types";
import { formatRelative, priorityLabel } from "@/lib/format";

// Supervisor dashboard. Three independent fetches start in parallel
// and each renders inside its own Suspense boundary so a slow widget
// doesn't block the others (per server-parallel-fetching +
// async-suspense-boundaries).

export const dynamic = "force-dynamic";

export default function SupervisorPage() {
  // Each panel kicks off its own fetch. Promises are constructed at
  // page render time and handed to the panel components, so they're
  // truly parallel -- if we awaited inline here, they'd serialise.
  const queue = gatewayJSON<{ queue: QueueDepth[] }>(
    "/v1/supervisor/queue", { cache: "no-store" });
  const agents = gatewayJSON<{ agents: AgentPresence[] }>(
    "/v1/supervisor/agents", { cache: "no-store" });
  const risk = gatewayJSON<{ tickets: AtRiskTicket[] }>(
    "/v1/supervisor/at-risk", { cache: "no-store" });

  return (
    <div className="grid h-screen grid-cols-3 gap-4 overflow-auto p-4">
      <Panel title="Queue depth">
        <Suspense fallback={<Skeleton lines={4} />}><QueueWidget p={queue} /></Suspense>
      </Panel>
      <Panel title="Agents online">
        <Suspense fallback={<Skeleton lines={6} />}><AgentsWidget p={agents} /></Suspense>
      </Panel>
      <Panel title="At risk">
        <Suspense fallback={<Skeleton lines={5} />}><RiskWidget p={risk} /></Suspense>
      </Panel>
    </div>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="flex h-full flex-col rounded border border-slate-200 bg-white">
      <header className="border-b border-slate-200 px-4 py-2 text-sm font-semibold">{title}</header>
      <div className="flex-1 overflow-auto p-4 text-sm">{children}</div>
    </section>
  );
}

async function QueueWidget({ p }: { p: Promise<{ queue: QueueDepth[] }> }) {
  const { queue } = await p;
  if (queue.length === 0) return <p className="text-slate-500">No tickets right now.</p>;
  return (
    <ul className="space-y-1">
      {queue.map((q) => (
        <li key={q.state} className="flex justify-between">
          <span className="text-slate-600">{q.state}</span>
          <span className="font-mono">{q.count}</span>
        </li>
      ))}
    </ul>
  );
}

async function AgentsWidget({ p }: { p: Promise<{ agents: AgentPresence[] }> }) {
  const { agents } = await p;
  if (agents.length === 0) return <p className="text-slate-500">No agents.</p>;
  return (
    <ul className="space-y-1">
      {agents.map((a) => (
        <li key={a.agent_id} className="flex items-center gap-2">
          <span className={`h-2 w-2 rounded-full ${a.status === "online" ? "bg-emerald-500" : "bg-slate-300"}`} />
          <span className="flex-1 truncate">{a.display_name}</span>
          <span className="text-xs text-slate-500">{a.current_load}/{a.max_concurrent}</span>
        </li>
      ))}
    </ul>
  );
}

async function RiskWidget({ p }: { p: Promise<{ tickets: AtRiskTicket[] }> }) {
  const { tickets } = await p;
  if (tickets.length === 0) return <p className="text-slate-500">Nothing at risk in the next 30 min.</p>;
  return (
    <ul className="space-y-2">
      {tickets.map((t) => (
        <li key={t.ticket_id} className="rounded border border-amber-200 bg-amber-50 p-2">
          <div className="flex justify-between text-xs">
            <span className="font-mono">{t.ticket_id.slice(0, 8)}</span>
            <span>{priorityLabel(t.priority)}</span>
          </div>
          <div className="text-xs text-slate-600">
            {t.first_response_due
              ? `FR due ${formatRelative(t.first_response_due)}`
              : t.resolution_due
              ? `resolution due ${formatRelative(t.resolution_due)}`
              : null}
          </div>
        </li>
      ))}
    </ul>
  );
}

function Skeleton({ lines }: { lines: number }) {
  return (
    <div className="space-y-2" aria-busy="true">
      {Array.from({ length: lines }).map((_, i) => (
        <div key={i} className="h-4 animate-pulse rounded bg-slate-100" />
      ))}
    </div>
  );
}
