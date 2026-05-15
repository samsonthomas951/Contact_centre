"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { sendMessage } from "@/app/(app)/tickets/[id]/actions";

// Client component for the reply box. The actual POST is a server
// action so the bearer token never leaves the server -- the form
// submission round-trips through Next, which calls the gateway.
export function Composer({ ticketId }: { ticketId: string }) {
  const [body, setBody] = useState("");
  const [direction, setDirection] = useState<"out" | "note">("out");
  const [pending, startTransition] = useTransition();
  const router = useRouter();

  const submit = () => {
    const trimmed = body.trim();
    if (!trimmed) return;
    // useTransition keeps the input responsive while the action runs;
    // the optimistic clear happens immediately, then router.refresh()
    // pulls the new message back in via the RSC.
    setBody("");
    startTransition(async () => {
      try {
        await sendMessage(ticketId, { direction, body: trimmed });
        router.refresh();
      } catch (e) {
        // On error, restore the body so the agent can retry.
        setBody(trimmed);
        // eslint-disable-next-line no-console
        console.error("send failed", e);
      }
    });
  };

  return (
    <form
      className="border-t border-slate-200 bg-white p-3"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="mb-2 flex gap-2 text-xs">
        <DirectionToggle value={direction} onChange={setDirection} />
      </div>
      <div className="flex items-end gap-2">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={(e) => {
            // Cmd/Ctrl + Enter sends. Shift+Enter inserts newline.
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
          rows={2}
          placeholder={direction === "note" ? "Internal note..." : "Reply to customer..."}
          className="flex-1 resize-none rounded border border-slate-300 px-3 py-2 text-sm focus:border-brand focus:outline-none"
          disabled={pending}
        />
        <button
          type="submit"
          disabled={pending || body.trim() === ""}
          className="rounded bg-brand px-4 py-2 text-sm font-semibold text-white disabled:opacity-50"
        >
          {pending ? "Sending..." : "Send"}
        </button>
      </div>
    </form>
  );
}

// Module-scope -- per rerender-no-inline-components.
function DirectionToggle({
  value,
  onChange,
}: {
  value: "out" | "note";
  onChange: (v: "out" | "note") => void;
}) {
  return (
    <>
      <ToggleBtn active={value === "out"} onClick={() => onChange("out")}>Reply</ToggleBtn>
      <ToggleBtn active={value === "note"} onClick={() => onChange("note")}>Internal note</ToggleBtn>
    </>
  );
}

function ToggleBtn({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-full px-2 py-0.5 " +
        (active ? "bg-slate-900 text-white" : "bg-slate-100 text-slate-700 hover:bg-slate-200")
      }
    >
      {children}
    </button>
  );
}
