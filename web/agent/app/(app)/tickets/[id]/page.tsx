import { Suspense } from "react";
import { notFound } from "next/navigation";
import { gatewayJSON, GatewayError } from "@/lib/api";
import type { Ticket, Message } from "@/lib/types";
import { Composer } from "@/components/Composer";
import { MessageThread } from "@/components/MessageThread";
import { TicketHeader } from "@/components/TicketHeader";
import { CustomerSidebar } from "@/components/CustomerSidebar";
import { TicketShortcuts } from "@/components/TicketShortcuts";
import { TicketTags } from "@/components/TicketTags";
import { auth } from "@/lib/auth";
import { loadCannedReplies, loadTags } from "./actions";

export const dynamic = "force-dynamic";

// Two independent fetches: the ticket itself and its message thread.
// Per async-parallel we start both with Promise.all so total wall
// time is max(t1, t2) rather than t1+t2. The header renders the
// ticket data once it resolves; the message thread is wrapped in
// Suspense so the user sees the header immediately even if the
// thread takes longer.
async function loadTicket(id: string): Promise<Ticket> {
  try {
    return await gatewayJSON<Ticket>(`/v1/tickets/${id}`, { cache: "no-store" });
  } catch (e) {
    if (e instanceof GatewayError && e.status === 404) notFound();
    throw e;
  }
}

async function loadMessages(id: string): Promise<Message[]> {
  const r = await gatewayJSON<{ messages: Message[] }>(
    `/v1/tickets/${id}/messages`,
    { cache: "no-store" },
  );
  return r.messages ?? [];
}

export default async function TicketPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  // meId is needed by macro actions ("assign: me"). Demo bearer
  // tokens carry the agent id in the second segment; in OIDC prod
  // this would come from id_token claims.
  const session = await auth();
  const tok = session?.accessToken ?? "";
  const meId = tok.startsWith("demo:") ? tok.split(":")[1] ?? "" : "";

  // Start all three fetches NOW (async-parallel). We only await the
  // ticket promise + canned replies (composer needs them at first
  // paint); the messages promise is handed to a Suspense boundary
  // so the header can paint while the thread is still in flight.
  const ticketPromise = loadTicket(id);
  const messagesPromise = loadMessages(id);
  const cannedRepliesPromise = loadCannedReplies();
  const tagsPromise = loadTags(id);

  const [ticket, cannedReplies, tags] = await Promise.all([
    ticketPromise,
    cannedRepliesPromise,
    tagsPromise,
  ]);

  return (
    <div className="flex min-h-screen flex-col md:h-screen md:flex-row">
      {/* Thread + composer column. On mobile this fills the viewport
          and the customer sidebar stacks below. On md+ it's the
          left column of a flex row. */}
      <div className="flex min-h-0 flex-1 flex-col bg-white">
        <TicketHeader ticket={ticket} />
        <div className="border-b border-slate-200 px-4 py-2 md:px-6">
          <TicketTags
            ticketId={id}
            attached={tags.attached}
            available={tags.available}
          />
        </div>
        <div className="flex-1 overflow-auto px-4 py-4 md:px-6">
          <Suspense fallback={<MessageSkeleton />}>
            <MessageThreadAsync messagesPromise={messagesPromise} />
          </Suspense>
        </div>
        <Composer ticketId={id} cannedReplies={cannedReplies} meId={meId} />
        <TicketShortcuts ticketId={id} currentState={ticket.state} />
      </div>
      {/* Customer profile column. Suspended so a slow customer fetch
          doesn't delay the thread paint. */}
      <Suspense fallback={<SidebarSkeleton />}>
        <CustomerSidebar ticketId={id} />
      </Suspense>
    </div>
  );
}

function SidebarSkeleton() {
  return (
    <aside className="w-full shrink-0 border-t border-slate-200 bg-slate-50 p-4 md:w-72 md:border-l md:border-t-0">
      <div className="h-4 w-32 animate-pulse rounded bg-slate-200" />
      <div className="mt-2 h-3 w-40 animate-pulse rounded bg-slate-200" />
      <div className="mt-6 h-3 w-20 animate-pulse rounded bg-slate-200" />
      <div className="mt-2 h-8 w-full animate-pulse rounded bg-slate-200" />
    </aside>
  );
}

// Resolves messagesPromise inside the Suspense boundary so the
// fallback shows while the thread loads.
async function MessageThreadAsync({
  messagesPromise,
}: {
  messagesPromise: Promise<Message[]>;
}) {
  const messages = await messagesPromise;
  return <MessageThread messages={messages} />;
}

function MessageSkeleton() {
  return (
    <div className="space-y-3" aria-busy="true">
      <div className="h-12 w-3/4 animate-pulse rounded bg-slate-100" />
      <div className="h-12 w-2/3 animate-pulse rounded bg-slate-100 ml-auto" />
      <div className="h-12 w-1/2 animate-pulse rounded bg-slate-100" />
    </div>
  );
}
