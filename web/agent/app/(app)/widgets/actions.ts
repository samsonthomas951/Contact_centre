"use server";

import { revalidatePath } from "next/cache";
import { gatewayPost, gatewayDelete, GatewayError } from "@/lib/api";

// Server actions for the widget admin page. Each runs on the Node
// server-side runtime so the bearer never reaches the browser.

export interface RegisterFormState {
  ok: boolean;
  error?: string;
}

export async function registerWidget(
  _prev: RegisterFormState | null,
  form: FormData,
): Promise<RegisterFormState> {
  const origin = String(form.get("origin") ?? "").trim();
  const displayName = String(form.get("display_name") ?? "").trim();
  const welcomeMessage = String(form.get("welcome_message") ?? "").trim();
  if (!origin || !displayName) {
    return { ok: false, error: "Origin and display name are required." };
  }
  try {
    await gatewayPost("/v1/onboarding/widgets",
      { origin, display_name: displayName, welcome_message: welcomeMessage });
  } catch (e) {
    if (e instanceof GatewayError) {
      if (e.status === 403) return { ok: false, error: "Admin role required." };
      if (e.status === 409) return { ok: false, error: "That origin is already registered." };
      if (e.status === 400) return { ok: false, error: "Origin must look like https://www.example.com (no path or query)." };
    }
    return { ok: false, error: "Could not register. Try again." };
  }
  revalidatePath("/widgets");
  return { ok: true };
}

export async function deleteWidget(id: string): Promise<void> {
  try {
    await gatewayDelete(`/v1/onboarding/widgets/${encodeURIComponent(id)}`);
  } catch {
    // Best-effort -- if the row's already gone we still want the
    // revalidate so the UI reflects reality.
  }
  revalidatePath("/widgets");
}
