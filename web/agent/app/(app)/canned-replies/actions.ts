"use server";

import { revalidatePath } from "next/cache";
import { gatewayPost, gatewayDelete, GatewayError } from "@/lib/api";

// CRUD server actions for the saved-replies management page. Errors
// come back as a typed { ok, error? } so the form can render them
// inline; the action never throws to the client tree.

export interface FormState {
  ok: boolean;
  error?: string;
}

export async function createReply(
  _prev: FormState | null,
  form: FormData,
): Promise<FormState> {
  const shortcut = String(form.get("shortcut") ?? "").trim();
  const title = String(form.get("title") ?? "").trim();
  const body = String(form.get("body") ?? "");
  const scope = String(form.get("scope") ?? "personal");
  const channel = String(form.get("channel") ?? "").trim();

  if (!shortcut || !title || !body) {
    return { ok: false, error: "shortcut, title, and body are required" };
  }
  try {
    await gatewayPost("/v1/canned-replies/", {
      shortcut, title, body,
      scope: scope === "tenant" ? "tenant" : "personal",
      channel: channel || undefined,
    });
  } catch (e) {
    if (e instanceof GatewayError) {
      if (e.status === 403) return { ok: false, error: "Only admins can create tenant-shared replies." };
      return { ok: false, error: e.message };
    }
    return { ok: false, error: "Save failed." };
  }
  revalidatePath("/canned-replies");
  return { ok: true };
}

export async function deleteReply(id: string): Promise<void> {
  try {
    await gatewayDelete(`/v1/canned-replies/${encodeURIComponent(id)}`);
  } catch {
    // best-effort -- the revalidate brings the UI back in sync
  }
  revalidatePath("/canned-replies");
}
