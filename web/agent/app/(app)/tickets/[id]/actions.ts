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

// runMacroActions executes a macro's post-send side-effects. Each
// step calls the appropriate gateway endpoint. Errors per-step are
// swallowed so a bad add_tag (e.g. tag missing) doesn't prevent the
// set_state that follows; the macro author can fix the definition.
//
// `meId` is the calling agent's UUID, used to resolve `assign: "me"`.
export async function runMacroActions(
  ticketId: string,
  actions: CannedReplyAction[],
  meId: string,
): Promise<void> {
  const { gatewayPost } = await import("@/lib/api");
  for (const step of actions) {
    try {
      if (step.type === "set_state" && step.to) {
        await gatewayPatch<{ to: string }, unknown>(
          `/v1/tickets/${ticketId}/state`,
          { to: step.to },
        );
      } else if (step.type === "add_tag" && step.tag_slug) {
        // The macro stores tag_slug for portability; resolve to
        // tag_id via the /tags listing. Cheap (1 cached fetch per
        // macro run); avoids embedding tenant-local UUIDs in seed
        // data.
        const list = await (await import("@/lib/api")).gatewayJSON<{
          tags: { id: string; slug: string }[];
        }>("/v1/tags/", { cache: "no-store" });
        const tag = list.tags?.find((t) => t.slug === step.tag_slug);
        if (tag) {
          await gatewayPost<{ tag_id: string }, void>(
            `/v1/tickets/${ticketId}/tags/`,
            { tag_id: tag.id },
          );
        }
      } else if (step.type === "assign" && step.to) {
        const agentID =
          step.to === "me"
            ? meId
            : step.to === "unassign"
              ? null
              : step.to;
        await gatewayPost<
          { op: string; ticket_ids: string[]; agent_id?: string },
          unknown
        >("/v1/tickets/bulk", {
          op: agentID ? "assign" : "unassign",
          ticket_ids: [ticketId],
          ...(agentID ? { agent_id: agentID } : {}),
        });
      }
    } catch {
      // Skip and continue -- the macro author can repair the recipe.
    }
  }
  revalidatePath(`/tickets/${ticketId}`);
  revalidatePath("/inbox");
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

// CannedReplyAction is one macro step. Mirrors the Go-side Action
// model. Only types the composer understands are surfaced in the UI;
// unknown types are ignored at execution time.
export interface CannedReplyAction {
  type: "set_state" | "add_tag" | "assign";
  to?: string;        // resolved/closed/... for set_state; me/unassign/uuid for assign
  tag_slug?: string;  // for add_tag
}

// CannedReply mirrors the gateway JSON. Owner is null for tenant-
// shared; the picker uses it only for a "(yours)" badge. `actions`
// turns a plain canned reply into a macro: a non-empty list fires
// side-effects after the message is sent.
export interface CannedReply {
  id: string;
  shortcut: string;
  title: string;
  body: string;
  channel?: string | null;
  owner_agent_id?: string | null;
  actions?: CannedReplyAction[];
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
