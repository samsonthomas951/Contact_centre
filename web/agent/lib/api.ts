import "server-only";
import { auth } from "@/lib/auth";

// Server-side API client for the Go gateway. We deliberately do not
// expose the access token to client components -- every fetch goes
// through a server component or server action.
//
// Per the React/Next 15 best-practices guide, we use the global fetch
// (which is React-cached per-request) for GETs so two RSCs that load
// the same URL on the same render dedupe to one network call.

const base = () => {
  const url = process.env.NEXT_PUBLIC_GATEWAY_URL;
  if (!url) throw new Error("NEXT_PUBLIC_GATEWAY_URL is required");
  return url.replace(/\/+$/, "");
};

async function bearer(): Promise<string> {
  const session = await auth();
  if (!session?.accessToken) {
    throw new Error("not authenticated");
  }
  return session.accessToken;
}

// FetchOptions extends RequestInit so callers can pass cache hints
// (e.g. { cache: 'no-store' } for live data, { next: { revalidate: 30 } }
// for the supervisor widgets).
export type FetchOptions = RequestInit & { next?: NextFetchRequestConfig };

// gatewayJSON GETs a JSON endpoint. Per server-cache-react, the global
// fetch is automatically deduplicated within one render -- two RSCs
// hitting the same URL produce one upstream call.
export async function gatewayJSON<T>(path: string, opts: FetchOptions = {}): Promise<T> {
  const tok = await bearer();
  const res = await fetch(base() + path, {
    ...opts,
    headers: {
      Authorization: `Bearer ${tok}`,
      Accept: "application/json",
      ...(opts.headers as Record<string, string> | undefined),
    },
  });
  if (!res.ok) {
    throw new GatewayError(res.status, `${res.status} ${res.statusText} on ${path}`);
  }
  return (await res.json()) as T;
}

// gatewayPost is the server-action helper. Body is JSON-marshalled.
export async function gatewayPost<TIn, TOut>(
  path: string,
  body: TIn,
  opts: FetchOptions = {},
): Promise<TOut> {
  const tok = await bearer();
  const res = await fetch(base() + path, {
    ...opts,
    method: "POST",
    headers: {
      Authorization: `Bearer ${tok}`,
      "Content-Type": "application/json",
      Accept: "application/json",
      ...(opts.headers as Record<string, string> | undefined),
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new GatewayError(res.status, `${res.status} ${res.statusText} on ${path}`);
  }
  // 204 -> undefined; the cast acknowledges TOut may be void.
  if (res.status === 204) return undefined as TOut;
  return (await res.json()) as TOut;
}

// gatewayUpload posts a multipart/form-data request and returns the
// JSON body. Used by the composer's attachment flow -- the file part
// must be added to the FormData by the caller.
export async function gatewayUpload<T>(path: string, form: FormData): Promise<T> {
  const tok = await bearer();
  const res = await fetch(base() + path, {
    method: "POST",
    headers: { Authorization: `Bearer ${tok}` },
    body: form,
  });
  if (!res.ok) {
    throw new GatewayError(res.status, `${res.status} ${res.statusText} on ${path}`);
  }
  return (await res.json()) as T;
}

// gatewayPatch sends a PATCH with a JSON body. Same shape as
// gatewayPost; kept distinct so call sites self-document intent.
export async function gatewayPatch<TIn, TOut>(
  path: string,
  body: TIn,
): Promise<TOut> {
  const tok = await bearer();
  const res = await fetch(base() + path, {
    method: "PATCH",
    headers: {
      Authorization: `Bearer ${tok}`,
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new GatewayError(res.status, `${res.status} ${res.statusText} on ${path}`);
  }
  if (res.status === 204) return undefined as TOut;
  return (await res.json()) as TOut;
}

// gatewayDelete fires a DELETE and returns nothing on 2xx. Used by the
// admin pages where the response body has nothing useful to render.
export async function gatewayDelete(path: string): Promise<void> {
  const tok = await bearer();
  const res = await fetch(base() + path, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${tok}` },
  });
  if (!res.ok && res.status !== 204) {
    throw new GatewayError(res.status, `${res.status} ${res.statusText} on ${path}`);
  }
}

export class GatewayError extends Error {
  constructor(public status: number, msg: string) {
    super(msg);
    this.name = "GatewayError";
  }
}
