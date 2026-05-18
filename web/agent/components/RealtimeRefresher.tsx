"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter, usePathname } from "next/navigation";

// One global subscriber per app load, mounted once in the (app) layout.
// On every inbound WS frame we trigger router.refresh() which re-runs
// the surrounding RSCs and pulls the new state. The composer's
// optimistic update means the agent sees their own outbound
// instantly; this listener picks up the customer's reply or the
// supervisor's escalation.
//
// Per client-event-listeners and the wider "one source of subscription"
// pattern: this is the only place in the app that opens a WS to
// /ws/agent. Other components consume state via the refreshed RSC
// rather than each opening their own socket.
//
// On top of refresh, this component also surfaces unseen inbound
// messages by:
//   1. Pulsing the document title to "(N) Inbox — …" while the tab is
//      hidden or the agent is off the relevant ticket page.
//   2. Optionally firing a desktop Notification if the agent has
//      granted permission (offered once via NotificationPrompt).
export function RealtimeRefresher({ wsURL }: { wsURL: string }) {
  const router = useRouter();
  const pathname = usePathname();
  const opened = useRef(false);
  const unseen = useRef(0);
  const baseTitle = useRef<string>("");
  const pathRef = useRef(pathname);
  pathRef.current = pathname;

  useEffect(() => {
    if (!baseTitle.current) baseTitle.current = document.title;
  }, []);

  // Clear the counter the moment the agent returns to the tab.
  useEffect(() => {
    function onVis() {
      if (document.visibilityState === "visible") {
        unseen.current = 0;
        applyTitle(baseTitle.current, 0);
      }
    }
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  useEffect(() => {
    if (opened.current) return;
    opened.current = true;
    if (!wsURL) return;

    let ws: WebSocket | null = null;
    let backoffMs = 1000;
    let cancelled = false;

    const connect = () => {
      if (cancelled) return;
      try {
        // The bearer token is on the NextAuth cookie; the browser
        // sends it automatically. The gateway's WS handler reads
        // Sec-WebSocket-Protocol or falls back to Authorization.
        ws = new WebSocket(wsURL);
      } catch {
        scheduleReconnect();
        return;
      }
      ws.onopen = () => {
        backoffMs = 1000; // reset on successful open
      };
      ws.onmessage = (ev) => {
        // Refresh on every frame -- the RSC fetch is the source of
        // truth; the WS is just a kick.
        router.refresh();

        // Parse for notification-worthy frames. Inbound customer
        // messages count; outbound / SLA / assignment do not (those
        // either originate from this agent or have less urgency).
        const frame = safeParse(ev.data);
        if (!frame || frame.type !== "message.new.inbound") return;

        const ticketID = frame.payload?.ticket_id as string | undefined;
        const onThisTicket =
          ticketID && pathRef.current === `/tickets/${ticketID}`;
        const tabVisible = document.visibilityState === "visible";
        // If the agent is staring at the ticket page right now, the
        // refresh above already gives them the new message inline --
        // skip the title pulse and desktop ping.
        if (tabVisible && onThisTicket) return;

        unseen.current += 1;
        applyTitle(baseTitle.current, unseen.current);

        if (!tabVisible && typeof Notification !== "undefined" &&
            Notification.permission === "granted") {
          try {
            // Tag per-ticket so three concurrent customer replies
            // surface as three distinct notifs (one per ticket)
            // rather than one collapsed banner. Re-pings on the
            // same ticket still replace prior notifs (correct).
            const n = new Notification("New customer message", {
              body: "Open the inbox to reply.",
              tag: ticketID ? `cc:ticket:${ticketID}` : "cc:inbox",
            });
            n.onclick = () => {
              // Click means "I'm dealing with it" — clear the
              // counter immediately rather than waiting for the
              // visibilitychange event, which would leave a stale
              // "(N) Inbox" in the title bar after navigation.
              unseen.current = 0;
              applyTitle(baseTitle.current, 0);
              window.focus();
              if (ticketID) router.push(`/tickets/${ticketID}`);
              n.close();
            };
          } catch {
            // Notifications can throw in some embedded contexts; we
            // don't want a broken notification to break the refresh.
          }
        }
      };
      ws.onclose = () => {
        scheduleReconnect();
      };
      ws.onerror = () => {
        // onclose fires next; let it handle the reconnect.
      };
    };

    const scheduleReconnect = () => {
      if (cancelled) return;
      const wait = Math.min(backoffMs, 30_000);
      setTimeout(connect, wait);
      backoffMs *= 2;
    };

    connect();
    return () => {
      cancelled = true;
      if (ws) {
        ws.onmessage = null;
        ws.onclose = null;
        ws.close();
      }
    };
  }, [wsURL, router]);

  return <NotificationPrompt />;
}

interface InboundFrame {
  type: string;
  payload?: { ticket_id?: string; tenant_id?: string };
}

function safeParse(raw: unknown): InboundFrame | null {
  if (typeof raw !== "string") return null;
  try {
    return JSON.parse(raw) as InboundFrame;
  } catch {
    return null;
  }
}

function applyTitle(base: string, n: number) {
  document.title = n > 0 ? `(${n}) ${base}` : base;
}

// NotificationPrompt asks the agent once whether they want desktop
// notifications. The banner only shows when permission is "default"
// (i.e., never asked) and dismisses for the session via localStorage.
const DISMISS_KEY = "notif:prompt:dismissed";

function NotificationPrompt() {
  const [show, setShow] = useState(false);

  useEffect(() => {
    if (typeof Notification === "undefined") return;
    if (Notification.permission !== "default") return;
    try {
      if (localStorage.getItem(DISMISS_KEY)) return;
    } catch {
      // ignore
    }
    setShow(true);
  }, []);

  if (!show) return null;

  const dismiss = () => {
    try {
      localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      // ignore
    }
    setShow(false);
  };

  const enable = async () => {
    try {
      await Notification.requestPermission();
    } catch {
      // ignore
    }
    dismiss();
  };

  return (
    <div className="fixed bottom-4 right-4 z-40 flex max-w-sm items-center gap-3 rounded-lg border border-slate-200 bg-white p-3 text-sm shadow-lg">
      <div className="flex-1 text-slate-700">
        Get desktop notifications when a customer replies?
      </div>
      <button
        type="button"
        onClick={enable}
        className="rounded bg-brand px-3 py-1 text-xs font-semibold text-white"
      >
        Enable
      </button>
      <button
        type="button"
        onClick={dismiss}
        className="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700"
      >
        Not now
      </button>
    </div>
  );
}
