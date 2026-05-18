"use client";

import { useEffect, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { changeState } from "@/app/(app)/tickets/[id]/actions";

// Ticket-page keyboard shortcuts:
//   r       focus the reply composer
//   e       resolve the ticket (state -> resolved)
//   j / ]   jump to the next ticket in the inbox order
//   k / [   jump to the previous ticket in the inbox order
//
// The j/k navigation uses the ordered ticket-id list the inbox
// persists to sessionStorage. If the user landed here from a deep
// link without visiting the inbox first, j/k are no-ops.

const NAV_KEY = "inbox:order";

export function TicketShortcuts({
  ticketId,
  currentState,
}: {
  ticketId: string;
  currentState: string;
}) {
  const router = useRouter();
  const [, startTransition] = useTransition();
  const [flash, setFlash] = useState<string | null>(null);
  const flashTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Show a transient toast-style hint when a shortcut fires. The user
  // gets visual confirmation that 'e' did something, not silence.
  const showFlash = (msg: string) => {
    setFlash(msg);
    if (flashTimer.current) clearTimeout(flashTimer.current);
    flashTimer.current = setTimeout(() => setFlash(null), 1500);
  };

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (shouldIgnore(e)) return;

      switch (e.key) {
        case "r": {
          e.preventDefault();
          const ta = document.querySelector<HTMLTextAreaElement>(
            "[data-composer-textarea]",
          );
          if (ta) {
            ta.focus();
            // Move caret to the end so the agent can type immediately.
            const end = ta.value.length;
            ta.setSelectionRange(end, end);
          }
          break;
        }
        case "e": {
          e.preventDefault();
          if (currentState === "resolved" || currentState === "closed") {
            showFlash("already resolved");
            return;
          }
          showFlash("resolving…");
          startTransition(async () => {
            try {
              await changeState(ticketId, "resolved");
              showFlash("resolved");
              router.refresh();
            } catch (err) {
              showFlash(err instanceof Error ? err.message : "resolve failed");
            }
          });
          break;
        }
        case "j":
        case "]": {
          e.preventDefault();
          const next = neighbour(ticketId, +1);
          if (next) router.push(`/tickets/${next}`);
          else showFlash("no next ticket");
          break;
        }
        case "k":
        case "[": {
          e.preventDefault();
          const prev = neighbour(ticketId, -1);
          if (prev) router.push(`/tickets/${prev}`);
          else showFlash("no previous ticket");
          break;
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ticketId, currentState, router]);

  if (!flash) return null;
  return (
    <div className="pointer-events-none fixed bottom-24 left-1/2 z-50 -translate-x-1/2 rounded-full bg-slate-900/90 px-4 py-1.5 text-xs text-white shadow-lg">
      {flash}
    </div>
  );
}

// neighbour reads the inbox order out of sessionStorage and returns
// the adjacent id (delta=+1 next, -1 prev). Returns null if the
// session list is missing or the current id isn't in it.
function neighbour(currentId: string, delta: 1 | -1): string | null {
  try {
    const raw = sessionStorage.getItem(NAV_KEY);
    if (!raw) return null;
    const ids = JSON.parse(raw) as string[];
    const i = ids.indexOf(currentId);
    if (i < 0) return null;
    const j = i + delta;
    if (j < 0 || j >= ids.length) return null;
    return ids[j];
  } catch {
    return null;
  }
}

function shouldIgnore(e: KeyboardEvent): boolean {
  if (e.metaKey || e.ctrlKey || e.altKey) return true;
  const t = e.target as HTMLElement | null;
  if (!t) return false;
  const tag = t.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (t.isContentEditable) return true;
  return false;
}
