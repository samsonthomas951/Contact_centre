import { Suspense } from "react";
import { notFound } from "next/navigation";
import { gatewayJSON, GatewayError } from "@/lib/api";
import type { Ticket, Message } from "@/lib/types";
import { Composer } from "@/components/Composer";
import { MessageThread } from "@/components/MessageThread";
import { TicketHeader } from "@/components/TicketHeader";

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

  // Start both fetches NOW (async-parallel). We only await the ticket
  // promise immediately; the messages promise is handed to a Suspense
  // boundary so the header can paint while the thread is still in
  // flight.
  const ticketPromise = loadTicket(id);
  const messagesPromise = loadMessages(id);

  const ticket = await ticketPromise;

  return (
    <div className="flex h-screen flex-col bg-white">
      <TicketHeader ticket={ticket} />
      <div className="flex-1 overflow-auto px-6 py-4">
        <Suspense fallback={<MessageSkeleton />}>
          <MessageThreadAsync messagesPromise={messagesPromise} />
        </Suspense>
      </div>
      <Composer ticketId={id} />
    </div>
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
