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
        <div className={"mt-1 text-[10px] " + (out ? "text-blue-100" : "text-slate-400")}>
          {formatRelative(m.created_at)}
        </div>
      </div>
    </li>
  );
}
