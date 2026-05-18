"use server";

import { gatewayPost, gatewayDelete, GatewayError } from "@/lib/api";
import { revalidatePath } from "next/cache";
import type { Tag } from "@/lib/types";

export interface FormState {
  ok?: boolean;
  error?: string;
}

// createTag wires the admin-only POST /v1/tags. On success the
// inbox + the page itself revalidate so the new chip appears.
export async function createTag(_: FormState | null, fd: FormData): Promise<FormState> {
  const slug = String(fd.get("slug") ?? "").trim();
  const name = String(fd.get("name") ?? "").trim();
  let color = String(fd.get("color") ?? "").trim();
  if (color.startsWith("#")) color = color.slice(1);

  if (!slug || !name) return { error: "Slug and name are required." };

  try {
    await gatewayPost<{ slug: string; name: string; color?: string }, Tag>(
      "/v1/tags/",
      { slug, name, color: color || undefined },
    );
  } catch (e) {
    return {
      error: e instanceof GatewayError ? e.message : "Failed to create.",
    };
  }
  revalidatePath("/tags");
  revalidatePath("/inbox");
  return { ok: true };
}

export async function deleteTag(id: string): Promise<void> {
  await gatewayDelete(`/v1/tags/${id}`);
  revalidatePath("/tags");
  revalidatePath("/inbox");
}
