"use client";

import { useActionState } from "react";
import { createReply, type FormState } from "./actions";

const CHANNELS = ["", "fb", "ig", "wa", "email", "widget", "voice", "x"];

// Client form for adding a new saved reply. Submits to the
// createReply server action and surfaces validation errors inline.
export function CreateForm({ canShare }: { canShare: boolean }) {
  const [state, action, pending] = useActionState<FormState | null, FormData>(
    createReply,
    null,
  );

  return (
    <form action={action} className="rounded border border-slate-200 bg-white p-4">
      <h2 className="text-sm font-semibold">Add saved reply</h2>
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <Field label="Shortcut" hint="lower_snake_case; what you type after /">
          <input name="shortcut" required pattern="^[a-z][a-z0-9_]{0,31}$"
            placeholder="refund_status"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm font-mono" />
        </Field>
        <Field label="Title" hint="Shown in the picker.">
          <input name="title" required maxLength={80}
            placeholder="Refund — checking"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm" />
        </Field>
        <Field label="Channel" hint="Optional. Limits the reply to one channel.">
          <select name="channel" className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm">
            {CHANNELS.map((c) => (
              <option key={c} value={c}>{c || "(any)"}</option>
            ))}
          </select>
        </Field>
        <Field label="Scope" hint={canShare ? "Tenant-shared = everyone can use." : "You can only save personal replies."}>
          <select name="scope" className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm">
            <option value="personal">Personal</option>
            {canShare && <option value="tenant">Tenant-shared</option>}
          </select>
        </Field>
      </div>
      <div className="mt-3">
        <label className="block text-sm font-medium text-slate-700">Body</label>
        <textarea name="body" required rows={4}
          placeholder="The exact text that will be inserted into the composer."
          className="mt-1 w-full rounded border border-slate-300 px-3 py-2 text-sm" />
      </div>

      <fieldset className="mt-4 rounded border border-dashed border-slate-300 p-3">
        <legend className="px-1 text-xs font-semibold uppercase tracking-wide text-slate-500">
          Macro actions <span className="font-normal lowercase">(optional)</span>
        </legend>
        <p className="text-xs text-slate-500">
          Run automatically after the reply is sent. Leave blank for a plain canned reply.
        </p>
        <div className="mt-2 grid gap-3 sm:grid-cols-3">
          <Field label="Set state" hint="Move the ticket on send.">
            <select name="macro_state" className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm">
              <option value="">(no change)</option>
              <option value="open">open</option>
              <option value="pending">pending</option>
              <option value="on_hold">on_hold</option>
              <option value="resolved">resolved</option>
              <option value="closed">closed</option>
            </select>
          </Field>
          <Field label="Add tag" hint="Slug (e.g. refund).">
            <input
              name="macro_tag"
              pattern="^([a-z][a-z0-9_]{0,31})?$"
              placeholder="refund"
              className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm font-mono"
            />
          </Field>
          <Field label="Assign" hint="'me', 'unassign', or agent uuid.">
            <input
              name="macro_assign"
              placeholder=""
              className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm font-mono"
            />
          </Field>
        </div>
      </fieldset>

      {state?.error && <p className="mt-3 text-sm text-red-600">{state.error}</p>}
      {state?.ok && <p className="mt-3 text-sm text-emerald-700">Saved.</p>}

      <button
        type="submit"
        disabled={pending}
        className="mt-4 rounded bg-brand px-4 py-2 text-sm font-semibold text-white hover:bg-brand-dark disabled:opacity-60"
      >
        {pending ? "Saving…" : "Add reply"}
      </button>
    </form>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block text-sm">
      <span className="font-medium text-slate-700">{label}</span>
      {hint && <span className="ml-2 text-xs text-slate-500">{hint}</span>}
      <div className="mt-1">{children}</div>
    </label>
  );
}
