"use client";

import { useState } from "react";
import { deleteReply } from "./actions";

export interface Reply {
  id: string;
  shortcut: string;
  title: string;
  body: string;
  channel?: string | null;
  owner_agent_id?: string | null;
  actions?: { type: string; to?: string; tag_slug?: string }[];
}

export function ReplyRow({ r, canDelete }: { r: Reply; canDelete: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const [deleting, setDeleting] = useState(false);

  async function onDelete() {
    if (!confirm(`Delete /${r.shortcut} (${r.title})?`)) return;
    setDeleting(true);
    await deleteReply(r.id);
  }

  return (
    <li className="border-b border-slate-100 px-4 py-3 last:border-0">
      <div className="flex items-baseline justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <code className="rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-700">/{r.shortcut}</code>
            <span className="text-sm font-medium text-slate-900">{r.title}</span>
            {!r.owner_agent_id && (
              <span className="rounded bg-emerald-100 px-1.5 py-0.5 text-[10px] text-emerald-800">tenant-shared</span>
            )}
            {r.actions && r.actions.length > 0 && (
              <span
                className="rounded bg-purple-100 px-1.5 py-0.5 text-[10px] text-purple-800"
                title={r.actions
                  .map((a) =>
                    a.type === "set_state"
                      ? `state→${a.to}`
                      : a.type === "add_tag"
                        ? `+#${a.tag_slug}`
                        : a.type === "assign"
                          ? `assign→${a.to}`
                          : a.type,
                  )
                  .join(", ")}
              >
                macro · {r.actions.length}
              </span>
            )}
            {r.channel && (
              <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] uppercase text-slate-600">
                {r.channel}
              </span>
            )}
          </div>
          <div className="mt-1 truncate text-xs text-slate-500">{r.body.split("\n")[0]}</div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700 hover:bg-slate-100"
          >
            {expanded ? "Hide" : "Preview"}
          </button>
          {canDelete && (
            <button
              type="button"
              onClick={onDelete}
              disabled={deleting}
              className="rounded border border-red-200 px-2 py-1 text-xs text-red-700 hover:bg-red-50 disabled:opacity-50"
            >
              {deleting ? "…" : "Delete"}
            </button>
          )}
        </div>
      </div>
      {expanded && (
        <pre className="mt-2 whitespace-pre-wrap rounded border border-slate-200 bg-slate-50 px-3 py-2 text-xs text-slate-700">
          {r.body}
        </pre>
      )}
    </li>
  );
}
