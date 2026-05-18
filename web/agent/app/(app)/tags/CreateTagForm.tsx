"use client";

import { useActionState } from "react";
import { createTag, type FormState } from "./actions";

export function CreateTagForm() {
  const [state, action, pending] = useActionState<FormState | null, FormData>(
    createTag,
    null,
  );

  return (
    <form action={action} className="rounded border border-slate-200 bg-white p-4">
      <h2 className="text-sm font-semibold">Add tag</h2>
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="Slug" hint="lower_snake_case">
          <input
            name="slug"
            required
            pattern="^[a-z][a-z0-9_]{0,31}$"
            placeholder="billing"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm font-mono"
          />
        </Field>
        <Field label="Name" hint="Shown on the chip">
          <input
            name="name"
            required
            maxLength={40}
            placeholder="Billing"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm"
          />
        </Field>
        <Field label="Color" hint="6-char hex (optional)">
          <input
            name="color"
            placeholder="f59e0b"
            pattern="^#?[0-9a-fA-F]{6}$"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm font-mono"
          />
        </Field>
      </div>

      {state?.error && <p className="mt-3 text-sm text-red-600">{state.error}</p>}
      {state?.ok && <p className="mt-3 text-sm text-emerald-700">Saved.</p>}

      <button
        type="submit"
        disabled={pending}
        className="mt-4 rounded bg-brand px-4 py-2 text-sm font-semibold text-white hover:bg-brand-dark disabled:opacity-60"
      >
        {pending ? "Saving…" : "Add tag"}
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
