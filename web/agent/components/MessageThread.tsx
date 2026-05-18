import type { Message } from "@/lib/types";
import { formatRelative } from "@/lib/format";

// Server component. Renders the thread reverse-chronological so the
// newest message lands at the bottom (the composer is below the
// scroll area; the scroll position is anchored bottom by the parent).
export function MessageThread({ messages }: { messages: Message[] }) {
  if (messages.length === 0) {
    return (
      <p className="py-8 text-center text-sm text-slate-500">
        No messages yet.
      </p>
    );
  }

  // Source data is newest-first per repo.ListMessages; reverse for UI.
  const ordered = [...messages].reverse();

  return (
    <ol className="space-y-2">
      {ordered.map((m) => (
        <Bubble key={m.id} m={m} />
      ))}
    </ol>
  );
}

// Module-scope per rerender-no-inline-components.
function Bubble({ m }: { m: Message }) {
  const out = m.direction === "out";
  const note = m.direction === "note";
  const hasAttachments = (m.attachments?.length ?? 0) > 0;
  return (
    <li
      className={`flex ${out ? "justify-end" : note ? "justify-center" : "justify-start"}`}
    >
      <div
        className={
          "max-w-[80%] rounded-2xl px-3 py-2 text-sm " +
          (note
            ? "border border-amber-200 bg-amber-50 text-amber-900 italic"
            : out
            ? "bg-brand text-white"
            : "border border-slate-200 bg-white text-slate-900")
        }
      >
        {/* Plain text only -- the wire format is text/plain bodies; we
            never render raw HTML from the customer here. */}
        <div className="whitespace-pre-wrap break-words">{m.body}</div>
        {hasAttachments && (
          <ul className="mt-1.5 flex flex-wrap gap-1">
            {m.attachments.map((docID) => (
              <AttachmentLink key={docID} docID={docID} onDark={out && !note} />
            ))}
          </ul>
        )}
        <div className={"mt-1 text-[10px] " + (out ? "text-blue-100" : "text-slate-400")}>
          {formatRelative(m.created_at)}
        </div>
      </div>
    </li>
  );
}

// AttachmentLink renders the doc-UUID chip as a link. Clicking opens
// the agent UI's download proxy route which forwards the bearer to
// the gateway's /v1/documents/{id}/download (a 302 to a presigned S3
// URL). The doc service is the source of truth for ACLs.
function AttachmentLink({ docID, onDark }: { docID: string; onDark: boolean }) {
  return (
    <li>
      <a
        href={`/api/documents/${encodeURIComponent(docID)}/download`}
        target="_blank"
        rel="noopener noreferrer"
        className={
          "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] " +
          (onDark
            ? "border-white/30 bg-white/10 text-white hover:bg-white/20"
            : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50")
        }
      >
        📎 <span>{docID.slice(0, 8)}</span>
      </a>
    </li>
  );
}
