"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

// Inbox keyboard navigation:
//   j/↓   focus next row
//   k/↑   focus previous row
//   Enter open the focused ticket
//   g     scroll to top (and focus first row)
//
// Also persists the current ordered ticket-id list to sessionStorage
// so the ticket page can implement prev/next without re-querying.

const NAV_KEY = "inbox:order";

export function InboxShortcuts({ ticketIds }: { ticketIds: string[] }) {
  const router = useRouter();
  const [active, setActive] = useState(0);
  const idsRef = useRef(ticketIds);
  idsRef.current = ticketIds;

  // Persist the list once per render; the ticket page reads it on
  // mount to know its neighbours. Wrapped in try/catch since
  // sessionStorage throws on some private-mode browsers.
  useEffect(() => {
    try {
      sessionStorage.setItem(NAV_KEY, JSON.stringify(ticketIds));
    } catch {
      // ignore
    }
  }, [ticketIds]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (shouldIgnore(e)) return;
      const ids = idsRef.current;
      if (ids.length === 0) return;

      switch (e.key) {
        case "j":
        case "ArrowDown":
          e.preventDefault();
          setActive((i) => Math.min(i + 1, ids.length - 1));
          break;
        case "k":
        case "ArrowUp":
          e.preventDefault();
          setActive((i) => Math.max(i - 1, 0));
          break;
        case "Enter":
          e.preventDefault();
          router.push(`/tickets/${ids[active]}`);
          break;
        case "g":
          e.preventDefault();
          setActive(0);
          break;
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [router, active]);

  // Move focus + scroll to the active row each time it changes. We
  // look the row up by data-ticket-id so the inbox doesn't need to
  // pass refs all the way down through its server component.
  useEffect(() => {
    const ids = idsRef.current;
    if (ids.length === 0) return;
    const id = ids[active];
    const row = document.querySelector<HTMLElement>(`[data-ticket-id="${id}"]`);
    if (!row) return;
    row.focus({ preventScroll: false });
    row.scrollIntoView({ block: "nearest" });
  }, [active]);

  return (
    <span
      className="ml-3 hidden text-[10px] text-slate-400 md:inline"
      title="j/k to move, Enter to open, g to top"
    >
      j/k Enter
    </span>
  );
}

// shouldIgnore filters out keystrokes the agent is aiming at an input
// (search bar, filter chips). Without this, typing 'j' in the search
// box would steal focus.
function shouldIgnore(e: KeyboardEvent): boolean {
  if (e.metaKey || e.ctrlKey || e.altKey) return true;
  const t = e.target as HTMLElement | null;
  if (!t) return false;
  const tag = t.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (t.isContentEditable) return true;
  return false;
}
