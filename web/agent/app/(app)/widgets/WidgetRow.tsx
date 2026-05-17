"use client";

import { useState } from "react";
import { deleteWidget } from "./actions";

export interface WidgetSite {
  id: string;
  origin: string;
  display_name: string;
  welcome_message: string;
  embed_key: string;
  active: boolean;
}

// Per-row card that reveals the copy-pasteable embed snippet on demand
// and confirms before deleting. Client-component because it owns local
// reveal/loading state; the actual delete still runs as a server action.
export function WidgetRow({ site, wsURL }: { site: WidgetSite; wsURL: string }) {
  const [showSnippet, setShowSnippet] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [copyState, setCopyState] = useState<"idle" | "copied">("idle");

  const snippet = buildSnippet(wsURL, site);

  async function onDelete() {
    if (!confirm(`Remove ${site.display_name}? The embed snippet on the site will stop working.`)) {
      return;
    }
    setDeleting(true);
    await deleteWidget(site.id);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(snippet);
      setCopyState("copied");
      setTimeout(() => setCopyState("idle"), 1500);
    } catch {
      // clipboard refused (no https, no permission); user can select manually.
    }
  }

  return (
    <li className="border-b border-slate-100 px-4 py-3 last:border-0">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-slate-800">{site.display_name}</div>
          <div className="truncate text-xs text-slate-500">{site.origin}</div>
          <div className="mt-1 font-mono text-xs text-slate-500">
            embed_key <code className="rounded bg-slate-100 px-1.5 py-0.5">{site.embed_key}</code>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className={"text-xs " + (site.active ? "text-emerald-700" : "text-slate-400")}>
            {site.active ? "active" : "inactive"}
          </span>
          <button
            type="button"
            onClick={() => setShowSnippet((s) => !s)}
            className="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700 hover:bg-slate-100"
          >
            {showSnippet ? "Hide snippet" : "Show snippet"}
          </button>
          <button
            type="button"
            onClick={onDelete}
            disabled={deleting}
            className="rounded border border-red-200 px-2 py-1 text-xs text-red-700 hover:bg-red-50 disabled:opacity-50"
          >
            {deleting ? "Removing…" : "Remove"}
          </button>
        </div>
      </div>

      {showSnippet ? (
        <div className="mt-3 rounded border border-slate-200 bg-slate-50">
          <div className="flex items-center justify-between border-b border-slate-200 px-3 py-1.5">
            <span className="text-xs font-medium text-slate-600">
              Paste this just before <code>&lt;/body&gt;</code> on every page of {site.origin}
            </span>
            <button
              type="button"
              onClick={copy}
              className="rounded border border-slate-300 bg-white px-2 py-1 text-xs font-medium hover:bg-slate-100"
            >
              {copyState === "copied" ? "Copied ✓" : "Copy"}
            </button>
          </div>
          <pre className="overflow-x-auto px-3 py-2 text-xs leading-relaxed text-slate-700">
            {snippet}
          </pre>
        </div>
      ) : null}
    </li>
  );
}

// buildSnippet renders the <script> tag the brand pastes into their HTML.
// Uses the runtime gateway URL so the snippet is correct whether the
// agent UI is on localhost or a real domain.
function buildSnippet(wsURL: string, site: WidgetSite): string {
  const widgetSrc = wsURL.replace(/^wss?:/, (m) => (m === "wss:" ? "https:" : "http:")) + "/widget.js";
  const endpoint = wsURL + "/ws/widget";
  return `<script
  src="${widgetSrc}"
  data-endpoint="${endpoint}"
  data-embed-key="${site.embed_key}"
  data-consent-text="By chatting you agree to our privacy notice."
  async></script>`;
}
