"use client";

import { useEffect, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import type { Tag } from "@/lib/types";
import {
  bulkState,
  bulkAssign,
  bulkAttachTag,
} from "@/app/(app)/inbox/actions";

export interface TeamMember {
  id: string;
  name: string;
}

// InboxBulkBar surfaces when the agent has selected at least one
// row via the checkbox in each inbox row. It listens at the window
// level for `change` events from `[data-ticket-select]` checkboxes
// so per-row React state isn't needed -- the bar owns the selection.
//
// Actions:
//   Assign ▾   assign all selected to an agent (or self)
//   Tag ▾      apply a tag to all selected
//   Resolve    mark all selected resolved
//   Close      close all selected
//   Clear      deselect everything
export function InboxBulkBar({
  tags,
  team,
  meId,
  canAssignOthers,
}: {
  tags: Tag[];
  team: TeamMember[];
  meId: string;
  canAssignOthers: boolean;
}) {
  const router = useRouter();
  const [, startTransition] = useTransition();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [openMenu, setOpenMenu] = useState<null | "assign" | "tag">(null);
  const [flash, setFlash] = useState<string | null>(null);
  const flashTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const showFlash = (msg: string) => {
    setFlash(msg);
    if (flashTimer.current) clearTimeout(flashTimer.current);
    flashTimer.current = setTimeout(() => setFlash(null), 1800);
  };

  // Listen for checkbox change events bubbling to the document.
  // Resilient to RSC refreshes -- new checkboxes inherit the
  // listener since it's mounted at the document level.
  useEffect(() => {
    function onChange(ev: Event) {
      const t = ev.target as HTMLInputElement | null;
      if (!t || t.dataset.ticketSelect === undefined) return;
      setSelected((prev) => {
        const next = new Set(prev);
        if (t.checked) next.add(t.value);
        else next.delete(t.value);
        return next;
      });
    }
    document.addEventListener("change", onChange);
    return () => document.removeEventListener("change", onChange);
  }, []);

  // After a refresh the checkboxes reset to unchecked; sync the
  // selection set by pruning IDs no longer in the DOM. We avoid
  // re-checking the boxes -- the agent's choice is "done with that
  // batch, start a fresh one".
  useEffect(() => {
    if (selected.size === 0) return;
    const present = new Set(
      Array.from(document.querySelectorAll<HTMLInputElement>("[data-ticket-select]")).map(
        (el) => el.value,
      ),
    );
    let drop = false;
    for (const id of selected) {
      if (!present.has(id)) {
        drop = true;
        break;
      }
    }
    if (drop) {
      setSelected((prev) => {
        const next = new Set<string>();
        for (const id of prev) if (present.has(id)) next.add(id);
        return next;
      });
    }
  });

  const clear = () => {
    document
      .querySelectorAll<HTMLInputElement>("[data-ticket-select]")
      .forEach((el) => (el.checked = false));
    setSelected(new Set());
    setOpenMenu(null);
  };

  const runState = (op: "resolve" | "close" | "reopen") => {
    const ids = Array.from(selected);
    if (ids.length === 0) return;
    setOpenMenu(null);
    showFlash(`${op}ing ${ids.length}…`);
    startTransition(async () => {
      try {
        const r = await bulkState(op, ids);
        const failed = r.failures?.length ?? 0;
        showFlash(
          failed > 0
            ? `${r.updated} ${op}d, ${failed} skipped`
            : `${r.updated} ${op}d`,
        );
        clear();
        router.refresh();
      } catch (e) {
        showFlash(e instanceof Error ? e.message : `${op} failed`);
      }
    });
  };

  const runAssign = (agentId: string | null) => {
    const ids = Array.from(selected);
    if (ids.length === 0) return;
    setOpenMenu(null);
    showFlash(agentId ? "assigning…" : "unassigning…");
    startTransition(async () => {
      try {
        const r = await bulkAssign(agentId, ids);
        showFlash(`${r.updated} updated`);
        clear();
        router.refresh();
      } catch (e) {
        showFlash(e instanceof Error ? e.message : "assign failed");
      }
    });
  };

  const runTag = (tagId: string) => {
    const ids = Array.from(selected);
    if (ids.length === 0) return;
    setOpenMenu(null);
    showFlash("tagging…");
    startTransition(async () => {
      try {
        await bulkAttachTag(tagId, ids);
        showFlash(`tagged ${ids.length}`);
        clear();
        router.refresh();
      } catch (e) {
        showFlash(e instanceof Error ? e.message : "tag failed");
      }
    });
  };

  if (selected.size === 0) {
    if (!flash) return null;
    return (
      <div className="pointer-events-none fixed bottom-6 left-1/2 z-50 -translate-x-1/2 rounded-full bg-slate-900/90 px-4 py-1.5 text-xs text-white shadow-lg">
        {flash}
      </div>
    );
  }

  return (
    <div className="fixed bottom-4 left-1/2 z-40 -translate-x-1/2 rounded-lg border border-slate-300 bg-white px-3 py-2 shadow-lg">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-medium text-slate-700">
          {selected.size} selected
        </span>
        <span className="text-slate-300">|</span>

        <Menu
          label="Assign"
          open={openMenu === "assign"}
          onToggle={() => setOpenMenu(openMenu === "assign" ? null : "assign")}
        >
          {meId && (
            <MenuItem onClick={() => runAssign(meId)}>Me</MenuItem>
          )}
          {canAssignOthers &&
            team
              .filter((m) => m.id !== meId)
              .map((m) => (
                <MenuItem key={m.id} onClick={() => runAssign(m.id)}>
                  {m.name}
                </MenuItem>
              ))}
          {canAssignOthers && (
            <MenuItem onClick={() => runAssign(null)} danger>
              Unassign
            </MenuItem>
          )}
        </Menu>

        <Menu
          label="Tag"
          open={openMenu === "tag"}
          onToggle={() => setOpenMenu(openMenu === "tag" ? null : "tag")}
        >
          {tags.length === 0 ? (
            <div className="px-3 py-2 text-xs text-slate-500">
              No tags exist.
            </div>
          ) : (
            tags.map((t) => (
              <MenuItem key={t.id} onClick={() => runTag(t.id)}>
                <span
                  className="mr-2 inline-block h-3 w-3 rounded-full align-middle"
                  style={{ backgroundColor: t.color ? `#${t.color}` : "#94a3b8" }}
                />
                {t.name}
              </MenuItem>
            ))
          )}
        </Menu>

        <button
          type="button"
          onClick={() => runState("resolve")}
          className="rounded bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-700"
        >
          Resolve
        </button>
        <button
          type="button"
          onClick={() => runState("close")}
          className="rounded bg-slate-700 px-3 py-1 text-xs font-semibold text-white hover:bg-slate-800"
        >
          Close
        </button>
        <button
          type="button"
          onClick={clear}
          className="rounded border border-slate-300 px-3 py-1 text-xs text-slate-700 hover:bg-slate-100"
        >
          Clear
        </button>
      </div>
      {flash && (
        <div className="mt-1 text-[11px] text-slate-500">{flash}</div>
      )}
    </div>
  );
}

function Menu({
  label,
  open,
  onToggle,
  children,
}: {
  label: string;
  open: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="relative">
      <button
        type="button"
        onClick={onToggle}
        className="rounded border border-slate-300 px-3 py-1 text-xs text-slate-700 hover:bg-slate-100"
      >
        {label} ▾
      </button>
      {open && (
        <div className="absolute bottom-full left-0 mb-1 max-h-64 w-48 overflow-y-auto rounded border border-slate-200 bg-white shadow-lg">
          {children}
        </div>
      )}
    </div>
  );
}

function MenuItem({
  children,
  onClick,
  danger,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-50 " +
        (danger ? "text-red-700" : "text-slate-800")
      }
    >
      {children}
    </button>
  );
}
