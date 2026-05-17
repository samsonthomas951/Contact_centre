"use client";

import { useActionState } from "react";
import { registerWidget, type RegisterFormState } from "./actions";

// Client form that posts to the registerWidget server action and
// surfaces validation errors inline. Lives at module scope so its
// identity stays stable across renders (rerender-no-inline-components).
export function RegisterForm() {
  const [state, formAction, pending] = useActionState<RegisterFormState | null, FormData>(
    registerWidget,
    null,
  );

  return (
    <form action={formAction} className="rounded border border-slate-200 bg-white p-4">
      <h2 className="text-sm font-semibold">Register a new site</h2>
      <p className="mt-1 text-xs text-slate-500">
        The origin is the exact scheme + host the embedded snippet runs on.
        It&apos;s checked against the WebSocket&apos;s <code>Origin</code>{" "}
        header on every visitor connect.
      </p>

      <div className="mt-3 grid gap-3">
        <Field label="Origin" hint="e.g. https://www.acme.co.ke (no path)">
          <input
            name="origin"
            type="url"
            required
            placeholder="https://www.acme.co.ke"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm"
          />
        </Field>
        <Field label="Display name">
          <input
            name="display_name"
            type="text"
            required
            placeholder="Acme — main site"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm"
          />
        </Field>
        <Field label="Welcome message" hint="Optional. Shown when a visitor opens the chat.">
          <input
            name="welcome_message"
            type="text"
            placeholder="Hi! How can we help?"
            className="w-full rounded border border-slate-300 px-3 py-1.5 text-sm"
          />
        </Field>
      </div>

      {state?.error ? (
        <p className="mt-3 text-sm text-red-600">{state.error}</p>
      ) : null}
      {state?.ok ? (
        <p className="mt-3 text-sm text-emerald-700">Registered. New site is below.</p>
      ) : null}

      <button
        type="submit"
        disabled={pending}
        className="mt-4 rounded bg-brand px-4 py-2 text-sm font-semibold text-white hover:bg-brand-dark disabled:opacity-60"
      >
        {pending ? "Registering…" : "Register site"}
      </button>
    </form>
  );
}

function Field({ label, hint, children }: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block text-sm">
      <span className="font-medium text-slate-700">{label}</span>
      {hint ? <span className="ml-2 text-xs text-slate-500">{hint}</span> : null}
      <div className="mt-1">{children}</div>
    </label>
  );
}
