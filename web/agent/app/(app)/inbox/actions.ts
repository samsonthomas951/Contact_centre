"use server";

import { gatewayPost } from "@/lib/api";
import { revalidatePath } from "next/cache";
import type { Tag } from "@/lib/types";

interface BulkResp {
  updated: number;
  failures?: { id: string; reason: string }[];
}

// bulkState resolves / closes / reopens many tickets in one call.
// Per-ticket transition errors come back as `failures` so the caller
// can surface a partial-success message.
export async function bulkState(
  op: "resolve" | "close" | "reopen",
  ticketIds: string[],
): Promise<BulkResp> {
  const r = await gatewayPost<{ op: string; ticket_ids: string[] }, BulkResp>(
    "/v1/tickets/bulk",
    { op, ticket_ids: ticketIds },
  );
  revalidatePath("/inbox");
  return r;
}

// bulkAssign assigns (or unassigns when agentId is null) many
// tickets in one call. Gateway enforces that non-supervisors can
// only self-assign.
export async function bulkAssign(
  agentId: string | null,
  ticketIds: string[],
): Promise<BulkResp> {
  const op = agentId ? "assign" : "unassign";
  const r = await gatewayPost<
    { op: string; ticket_ids: string[]; agent_id?: string },
    BulkResp
  >("/v1/tickets/bulk", {
    op,
    ticket_ids: ticketIds,
    ...(agentId ? { agent_id: agentId } : {}),
  });
  revalidatePath("/inbox");
  return r;
}

// bulkAttachTag iterates per-ticket POSTs because the gateway's
// /tags endpoint is per-ticket. Small N (<=200 is the bulk cap)
// makes Promise.all fast enough that a batch endpoint isn't worth
// the extra surface. Per-call 4xx (e.g. tag already attached) is
// swallowed; the caller only sees a count.
export async function bulkAttachTag(tagId: string, ticketIds: string[]): Promise<{ updated: number }> {
  await Promise.all(
    ticketIds.map((tid) =>
      gatewayPost<{ tag_id: string }, void>(
        `/v1/tickets/${tid}/tags/`,
        { tag_id: tagId },
      ).catch(() => undefined),
    ),
  );
  revalidatePath("/inbox");
  return { updated: ticketIds.length };
}

export type { Tag };
