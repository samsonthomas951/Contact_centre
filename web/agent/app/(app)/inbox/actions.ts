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
// /tags endpoint is per-ticket. Bounded concurrency (8 in flight)
// keeps a 200-ticket batch from hammering the gateway pool while
// still finishing in well under a second over a healthy link.
// Per-call 4xx (e.g. tag already attached) is swallowed; the caller
// only sees a count of attempted writes.
const BULK_TAG_CONCURRENCY = 8;

export async function bulkAttachTag(tagId: string, ticketIds: string[]): Promise<{ updated: number }> {
  await runBounded(ticketIds, BULK_TAG_CONCURRENCY, (tid) =>
    gatewayPost<{ tag_id: string }, void>(
      `/v1/tickets/${tid}/tags/`,
      { tag_id: tagId },
    ).catch(() => undefined),
  );
  revalidatePath("/inbox");
  return { updated: ticketIds.length };
}

// runBounded executes `task` over `items` with at most `limit`
// outstanding. Plain worker-pool; finishes when every task settles.
async function runBounded<T>(
  items: T[],
  limit: number,
  task: (item: T) => Promise<unknown>,
): Promise<void> {
  let i = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (true) {
      const idx = i++;
      if (idx >= items.length) return;
      await task(items[idx]);
    }
  });
  await Promise.all(workers);
}

export type { Tag };
