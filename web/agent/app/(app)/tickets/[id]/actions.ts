"use server";

import { gatewayPost } from "@/lib/api";
import { revalidatePath } from "next/cache";
import type { Message } from "@/lib/types";

// sendMessage is the server action the composer calls. The bearer
// token rides through gatewayPost (server-only); the client component
// never sees it. Per server-auth-actions: server actions are
// authenticated via the same NextAuth session every other server-side
// fetch uses, so the action inherits the page's auth without
// re-checking.
export async function sendMessage(
  ticketId: string,
  input: { direction: "out" | "note"; body: string },
): Promise<Message> {
  // gatewayPost throws on non-2xx; the composer catches and surfaces
  // the error as a console.error + body restore.
  const m = await gatewayPost<typeof input, Message>(
    `/v1/tickets/${ticketId}/messages`,
    input,
  );
  // Invalidate the cache so the next router.refresh() pulls the
  // freshly-appended message from the gateway. We do this even though
  // the page is dynamic; revalidatePath makes the contract explicit.
  revalidatePath(`/tickets/${ticketId}`);
  return m;
}
