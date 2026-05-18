"use server";

import { gatewayPost, gatewayUpload } from "@/lib/api";
import { revalidatePath } from "next/cache";
import type { Message } from "@/lib/types";

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
