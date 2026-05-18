"use client";

import { useRouter, useSearchParams, usePathname } from "next/navigation";
import { useTransition } from "react";
import type { Tag } from "@/lib/types";

// FilterBar drives the inbox via URL search params so every filter is
// shareable, bookmarkable, and survives a refresh. Inputs are
// uncontrolled with key={searchParams.toString()} so a URL edit (back/
// forward, copy-paste link) resets them; updates go through the router
// rather than local state.
//
// One-component design keeps the inbox page a pure RSC; this client
// island is the only piece that mutates the URL.

const CHANNELS: { value: string; label: string }[] = [
  { value: "fb",     label: "Facebook" },
  { value: "ig",     label: "Instagram" },
  { value: "wa",     label: "WhatsApp" },
  { value: "email",  label: "Email" },
  { value: "widget", label: "Widget" },
  { value: "x",      label: "X" },
  { value: "voice",  label: "Voice" },
];

const STATES: { value: string; label: string }[] = [
  { value: "new",      label: "New" },
  { value: "open",     label: "Open" },
  { value: "pending",  label: "Pending" },
  { value: "on_hold",  label: "On hold" },
  { value: "reopened", label: "Reopened" },
];

const ASSIGNED: { value: string; label: string }[] = [
  { value: "",            label: "Everyone" },
  { value: "mine",        label: "Mine" },
  { value: "unassigned",  label: "Unassigned" },
];

export function FilterBar({ tags = [] }: { tags?: Tag[] }) {
  const router = useRouter();
  const pathname = usePathname();
  const sp = useSearchParams();
  const [pending, startTransition] = useTransition();

  // Merge new params into the existing URL so picking a channel
  // doesn't blow away the search query, and vice versa.
  function update(patch: Record<string, string | string[] | null>) {
    const next = new URLSearchParams(sp.toString());
    for (const [k, v] of Object.entries(patch)) {
      if (v === null || v === "" || (Array.isArray(v) && v.length === 0)) {
        next.delete(k);
      } else if (Array.isArray(v)) {
        next.delete(k);
        for (const item of v) next.append(k, item);
      } else {
        next.set(k, v);
      }
    }
    const q = next.toString();
    startTransition(() => router.push(q ? `${pathname}?${q}` : pathname));
  }

  const selectedChannels = sp.getAll("channel");
  const selectedStates   = sp.getAll("state");
  const selectedTags     = sp.getAll("tag");
  const assigned         = sp.get("assigned") ?? "";
  const q                = sp.get("q") ?? "";

  return (
    <div className="border-b border-slate-200 bg-white px-4 py-3 space-y-3 md:px-6">
      <div className="flex items-center gap-2">
        <input
          key={`q-${q}`}
          defaultValue={q}
          placeholder="Search messages..."
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              update({ q: (e.currentTarget as HTMLInputElement).value });
            }
          }}
          onBlur={(e) => {
            if (e.currentTarget.value !== q) update({ q: e.currentTarget.value });
          }}
          className="flex-1 rounded border border-slate-300 px-3 py-1.5 text-sm focus:border-brand focus:outline-none"
        />
        <select
          key={`a-${assigned}`}
          defaultValue={assigned}
          onChange={(e) => update({ assigned: e.target.value })}
          className="rounded border border-slate-300 px-2 py-1.5 text-sm"
        >
          {ASSIGNED.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
        {pending && <span className="text-xs text-slate-400">updating…</span>}
      </div>

      <div className="flex flex-wrap gap-1.5 text-xs">
        <span className="self-center text-slate-500">Channels:</span>
        {CHANNELS.map((c) => (
          <Toggle
            key={c.value}
            label={c.label}
            active={selectedChannels.includes(c.value)}
            onClick={() => {
              const next = selectedChannels.includes(c.value)
                ? selectedChannels.filter((x) => x !== c.value)
                : [...selectedChannels, c.value];
              update({ channel: next });
            }}
          />
        ))}
      </div>

      <div className="flex flex-wrap gap-1.5 text-xs">
        <span className="self-center text-slate-500">States:</span>
        {STATES.map((s) => (
          <Toggle
            key={s.value}
            label={s.label}
            active={selectedStates.includes(s.value)}
            onClick={() => {
              const next = selectedStates.includes(s.value)
                ? selectedStates.filter((x) => x !== s.value)
                : [...selectedStates, s.value];
              update({ state: next });
            }}
          />
        ))}
        {(selectedChannels.length > 0 ||
          selectedStates.length > 0 ||
          selectedTags.length > 0 ||
          q !== "" ||
          assigned !== "") && (
          <button
            type="button"
            onClick={() =>
              update({ channel: [], state: [], tag: [], q: null, assigned: null })
            }
            className="ml-auto rounded px-2 py-1 text-xs text-slate-500 hover:bg-slate-100"
          >
            Clear all
          </button>
        )}
      </div>

      {tags.length > 0 && (
        <div className="flex flex-wrap gap-1.5 text-xs">
          <span className="self-center text-slate-500">Tags:</span>
          {tags.map((t) => (
            <TagToggle
              key={t.id}
              tag={t}
              active={selectedTags.includes(t.slug)}
              onClick={() => {
                const next = selectedTags.includes(t.slug)
                  ? selectedTags.filter((x) => x !== t.slug)
                  : [...selectedTags, t.slug];
                update({ tag: next });
              }}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// TagToggle paints the active state with the tag's own colour so the
// active filter visually matches the chips on the rows.
function TagToggle({
  tag,
  active,
  onClick,
}: {
  tag: Tag;
  active: boolean;
  onClick: () => void;
}) {
  const fill = tag.color ? `#${tag.color}` : "#94a3b8";
  return (
    <button
      type="button"
      onClick={onClick}
      style={
        active
          ? { backgroundColor: fill, borderColor: fill, color: "white" }
          : undefined
      }
      className={
        "rounded-full border px-2.5 py-0.5 text-xs " +
        (active ? "" : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50")
      }
    >
      {tag.name}
    </button>
  );
}

// Module-scope per rerender-no-inline-components.
function Toggle({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-full border px-2.5 py-0.5 text-xs " +
        (active
          ? "border-brand bg-brand text-white"
          : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50")
      }
    >
      {label}
    </button>
  );
}
