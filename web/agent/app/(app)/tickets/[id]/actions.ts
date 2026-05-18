"use server";

import { gatewayPost, gatewayPatch, gatewayUpload, gatewayDelete } from "@/lib/api";
import { revalidatePath } from "next/cache";
import type { Message, Ticket, Tag } from "@/lib/types";

// sendMessage is the server action the composer calls. The bearer
// token rides through gatewayPost (server-only); the client never
// sees it. `attachments` is a list of document UUIDs returned by
// prior uploadAttachment calls -- the message handler stores them on
// messages.attachments so the thread can render download links.
export async function sendMessage(
  ticketId: string,
  input: { direction: "out" | "note"; body: string; attachments?: string[] },
): Promise<Message> {
  const m = await gatewayPost<typeof input, Message>(
    `/v1/tickets/${ticketId}/messages`,
    { ...input, attachments: input.attachments ?? [] },
  );
  revalidatePath(`/tickets/${ticketId}`);
  return m;
}

// changeState moves the ticket to a new state. The gateway enforces
// which transitions are legal (e.g., resolved → closed allowed,
// closed → new not). Called by the `e` keyboard shortcut.
export async function changeState(
  ticketId: string,
  to: "open" | "pending" | "resolved" | "closed",
): Promise<Ticket> {
  const t = await gatewayPatch<{ to: string }, Ticket>(
    `/v1/tickets/${ticketId}/state`,
    { to },
  );
  revalidatePath(`/tickets/${ticketId}`);
  revalidatePath("/inbox");
  return t;
}

// UploadedDoc is the minimum the composer needs to render a chip
// with filename + size and later resolve a download URL.
export interface UploadedDoc {
  id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
}

// uploadAttachment streams one file to /v1/documents and returns the
// minted Document. Ticket linkage is enforced server-side so the
// audit ledger ties the upload to this conversation.
export async function uploadAttachment(
  ticketId: string,
  form: FormData,
): Promise<UploadedDoc> {
  form.set("ticket_id", ticketId);
  return gatewayUpload<UploadedDoc>("/v1/documents", form);
}

// CannedReply mirrors the gateway JSON. Owner is null for tenant-
// shared; the picker uses it only for a "(yours)" badge.
export interface CannedReply {
  id: string;
  shortcut: string;
  title: string;
  body: string;
  channel?: string | null;
  owner_agent_id?: string | null;
}

// loadTags returns both the tags attached to this ticket and the
// full catalogue, in parallel, so the TicketTags editor can render
// a working picker without a second round-trip on user interaction.
export async function loadTags(ticketId: string): Promise<{
  attached: Tag[];
  available: Tag[];
}> {
  const api = await import("@/lib/api");
  try {
    const [a, b] = await Promise.all([
      api.gatewayJSON<{ tags: Tag[] }>(`/v1/tickets/${ticketId}/tags/`, {
        cache: "no-store",
      }),
      api.gatewayJSON<{ tags: Tag[] }>("/v1/tags/", { cache: "no-store" }),
    ]);
    return { attached: a.tags ?? [], available: b.tags ?? [] };
  } catch {
    return { attached: [], available: [] };
  }
}

// attachTag posts to the per-ticket /tags route. Any authed role
// may call; the gateway validates ticket + tag belong to the tenant.
export async function attachTag(ticketId: string, tagId: string): Promise<void> {
  await gatewayPost<{ tag_id: string }, void>(
    `/v1/tickets/${ticketId}/tags/`,
    { tag_id: tagId },
  );
  revalidatePath(`/tickets/${ticketId}`);
  revalidatePath("/inbox");
}

// detachTag removes the link. 404 on a missing link is intentionally
// not treated as an error -- the desired end state is "not attached".
export async function detachTag(ticketId: string, tagId: string): Promise<void> {
  await gatewayDelete(`/v1/tickets/${ticketId}/tags/${tagId}`);
  revalidatePath(`/tickets/${ticketId}`);
  revalidatePath("/inbox");
}

// loadCannedReplies returns the visible set for the calling agent.
// Pulled once per ticket page render; the composer then filters
// client-side as the agent types after "/".
export async function loadCannedReplies(): Promise<CannedReply[]> {
  try {
    const res = await (await import("@/lib/api")).gatewayJSON<{
      replies: CannedReply[];
    }>("/v1/canned-replies/", { cache: "no-store" });
    return res.replies ?? [];
  } catch {
    return [];
  }
}
