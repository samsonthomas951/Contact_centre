import type { Ticket } from "@/lib/types";
import { priorityLabel, formatRelative } from "@/lib/format";

// Server component -- no interactivity, so it renders on the server
// and ships zero JS for itself.
export function TicketHeader({ ticket }: { ticket: Ticket }) {
  return (
    <header className="border-b border-slate-200 px-4 py-3 md:px-6">
      <div className="flex items-center gap-3">
        <h1 className="font-semibold">Ticket {ticket.id.slice(0, 8)}</h1>
        <Badge>{ticket.state}</Badge>
        <Badge>{priorityLabel(ticket.priority)}</Badge>
        <span className="ml-auto text-xs text-slate-500">
          opened {formatRelative(ticket.created_at)}
        </span>
      </div>
      {ticket.sla_first_response_due && !ticket.first_response_at && (
        <div className="mt-1 text-xs text-amber-700">
          First response due {formatRelative(ticket.sla_first_response_due)}
        </div>
      )}
    </header>
  );
}

function Badge({ children }: { children: React.ReactNode }) {
  return (
    <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-700">
      {children}
    </span>
  );
}
