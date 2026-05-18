"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import type { Tag } from "@/lib/types";
import { TagChip } from "./TagChip";
import { attachTag, detachTag } from "@/app/(app)/tickets/[id]/actions";

// TicketTags renders the chips on the ticket header and lets the
// agent add/remove. The "+ Add" button expands into a small picker
// of available tags filtered by what isn't already attached.
export function TicketTags({
  ticketId,
  attached,
  available,
}: {
  ticketId: string;
  attached: Tag[];
  available: Tag[];
}) {
  const router = useRouter();
  const [, startTransition] = useTransition();
  const [pickerOpen, setPickerOpen] = useState(false);

  const attachedIds = new Set(attached.map((t) => t.id));
  const candidates = available.filter((t) => !attachedIds.has(t.id));

  const onAdd = (tag: Tag) => {
    setPickerOpen(false);
    startTransition(async () => {
      try {
        await attachTag(ticketId, tag.id);
        router.refresh();
      } catch {
        // server action surfaces via revalidatePath; on transient
        // failure the agent can just retry.
      }
    });
  };

  const onRemove = (tag: Tag) => {
    startTransition(async () => {
      try {
        await detachTag(ticketId, tag.id);
        router.refresh();
      } catch {
        // ignore -- next refresh shows the true state
      }
    });
  };

  return (
    <div className="relative flex flex-wrap items-center gap-1.5">
      {attached.map((t) => (
        <TagChip key={t.id} tag={t} size="md" onRemove={() => onRemove(t)} />
      ))}
      <button
        type="button"
        onClick={() => setPickerOpen((v) => !v)}
        className="rounded-full border border-dashed border-slate-300 px-2 py-0.5 text-xs text-slate-500 hover:bg-slate-100"
      >
        + Add tag
      </button>
      {pickerOpen && (
        <div className="absolute top-full left-0 z-30 mt-1 max-h-64 w-56 overflow-y-auto rounded border border-slate-200 bg-white text-sm shadow-lg">
          {candidates.length === 0 ? (
            <div className="px-3 py-2 text-xs text-slate-500">
              {available.length === 0
                ? "No tags exist yet."
                : "All available tags are already attached."}
            </div>
          ) : (
            <ul>
              {candidates.map((t) => (
                <li key={t.id}>
                  <button
                    type="button"
                    onClick={() => onAdd(t)}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-slate-50"
                  >
                    <span
                      className="inline-block h-3 w-3 rounded-full"
                      style={{ backgroundColor: t.color ? `#${t.color}` : "#94a3b8" }}
                    />
                    <span>{t.name}</span>
                    <span className="ml-auto text-[10px] text-slate-400">#{t.slug}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
