import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";

// Proxies the browser's GET /api/documents/<id>/download to the
// gateway's /v1/documents/<id>/download, adding the agent's bearer.
// The gateway responds with `{url: <presigned MinIO URL>}`; we 302
// the browser to that URL so the bytes stream straight from object
// storage instead of through us. Presigned URLs expire in 5 minutes.

export const dynamic = "force-dynamic";

export async function GET(
  _req: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  const session = await auth();
  if (!session?.accessToken) {
    return new NextResponse("unauthorized", { status: 401 });
  }

  const base = (process.env.NEXT_PUBLIC_GATEWAY_URL ?? "").replace(/\/+$/, "");
  const upstream = await fetch(
    `${base}/v1/documents/${encodeURIComponent(id)}/download`,
    { headers: { Authorization: `Bearer ${session.accessToken}` } },
  );
  if (!upstream.ok) {
    return new NextResponse(`gateway returned ${upstream.status}`, {
      status: upstream.status,
    });
  }
  const body: { url?: string } = await upstream.json();
  if (!body.url) {
    return new NextResponse("gateway returned no url", { status: 502 });
  }
  // Rewrite MinIO's internal host (minio:9000) to the host the
  // browser can reach. NEXT_PUBLIC_MINIO_URL is set by the demo env;
  // production with a real CDN doesn't need the rewrite because the
  // gateway returns the public host directly.
  const internalHost = process.env.MINIO_INTERNAL_HOST ?? "minio:9000";
  const publicHost = process.env.NEXT_PUBLIC_MINIO_URL ?? `http://localhost:9000`;
  const finalURL = body.url.replace(`http://${internalHost}`, publicHost);
  return NextResponse.redirect(finalURL, 302);
}
