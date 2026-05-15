"use client";

import { useEffect, useRef } from "react";
import { useRouter } from "next/navigation";

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
export function RealtimeRefresher({ wsURL }: { wsURL: string }) {
  const router = useRouter();
  const opened = useRef(false);

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
      ws.onmessage = () => {
        // We don't bother parsing the payload here -- any inbound
        // frame is a signal to re-render. The RSC fetch is the
        // source of truth; the WS is just a kick.
        router.refresh();
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

  return null;
}
