"use client";

import { useState } from "react";
import type { Tag } from "@/lib/types";
import { TagChip } from "@/components/TagChip";
import { deleteTag } from "./actions";

export function TagRow({ t, canDelete }: { t: Tag; canDelete: boolean }) {
  const [deleting, setDeleting] = useState(false);

  async function onDelete() {
    if (!confirm(`Delete #${t.slug} (${t.name})? This removes it from every ticket.`))
      return;
    setDeleting(true);
    await deleteTag(t.id);
  }

  return (
    <li className="flex items-center gap-3 border-b border-slate-100 px-4 py-3 last:border-0">
      <TagChip tag={t} size="md" />
      <code className="text-xs text-slate-500">#{t.slug}</code>
      <span className="ml-auto text-xs text-slate-500">
        {t.color ? `#${t.color}` : "no color"}
      </span>
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
    </li>
  );
}
