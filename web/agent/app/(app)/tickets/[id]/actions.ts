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

// MacroResult tells the Composer which steps landed and which
// fell over so the agent gets a useful toast instead of silent
// success. Steps run sequentially because order matters
// (set_state -> add_tag would tag a resolved ticket, which is
// usually fine, but the macro author writes the recipe top-down).
export interface MacroResult {
  ran: number;
  failed: { step: number; type: string; reason: string }[];
}

// runMacroActions executes a macro's post-send side-effects. The
// tag_slug -> tag_id resolution happens ONCE per call (cached for
// the whole batch of steps) rather than per add_tag.
//
// `meId` is the calling agent's UUID, used to resolve `assign: "me"`.
export async function runMacroActions(
  ticketId: string,
  actions: CannedReplyAction[],
  meId: string,
): Promise<MacroResult> {
  const api = await import("@/lib/api");
  const result: MacroResult = { ran: 0, failed: [] };

  // Resolve tag catalogue once if any add_tag step exists. Cuts
  // N round-trips for an N-add-tag macro to one.
  let tagsBySlug: Map<string, string> | null = null;
  const needsTags = actions.some((a) => a.type === "add_tag" && a.tag_slug);
  if (needsTags) {
    try {
      const list = await api.gatewayJSON<{
        tags: { id: string; slug: string }[];
      }>("/v1/tags/", { cache: "no-store" });
      tagsBySlug = new Map((list.tags ?? []).map((t) => [t.slug, t.id]));
    } catch {
      tagsBySlug = new Map(); // empty -> all add_tag steps fail with "tag not found"
    }
  }

  for (let i = 0; i < actions.length; i++) {
    const step = actions[i];
    try {
      if (step.type === "set_state" && step.to) {
        await gatewayPatch<{ to: string }, unknown>(
          `/v1/tickets/${ticketId}/state`,
          { to: step.to },
        );
      } else if (step.type === "add_tag" && step.tag_slug) {
        const tagId = tagsBySlug?.get(step.tag_slug);
        if (!tagId) {
          throw new Error(`tag "${step.tag_slug}" not found`);
        }
        await gatewayPost<{ tag_id: string }, void>(
          `/v1/tickets/${ticketId}/tags/`,
          { tag_id: tagId },
        );
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
      } else {
        // Unknown action type: not an error per se -- forwards
        // compatibility -- but worth surfacing so the author can
        // upgrade the client.
        result.failed.push({
          step: i,
          type: step.type,
          reason: "unknown action type",
        });
        continue;
      }
      result.ran++;
    } catch (e) {
      result.failed.push({
        step: i,
        type: step.type,
        reason: e instanceof Error ? e.message : "step failed",
      });
      // Keep going: a missing tag shouldn't block a state change.
    }
  }
  revalidatePath(`/tickets/${ticketId}`);
  revalidatePath("/inbox");
  return result;
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
