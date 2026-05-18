import { gatewayJSON, GatewayError } from "@/lib/api";
import { auth } from "@/lib/auth";
import type { Tag } from "@/lib/types";
import { CreateTagForm } from "./CreateTagForm";
import { TagRow } from "./TagRow";

// Tag catalogue management. Listing is everyone; create + delete are
// admin-only (gateway enforces; the form just hides for non-admins).

export const dynamic = "force-dynamic";

export default async function TagsPage() {
  const session = await auth();
  if (!session?.accessToken) return null;

  const tok = session.accessToken;
  const role = tok.startsWith("demo:") ? tok.split(":")[3] ?? "" : "";
  const isAdmin = role.split(",").includes("admin");

  let tags: Tag[] = [];
  let error: string | null = null;
  try {
    const res = await gatewayJSON<{ tags: Tag[] }>("/v1/tags/", { cache: "no-store" });
    tags = res.tags ?? [];
  } catch (e) {
    error = e instanceof GatewayError ? e.message : "Failed to load.";
  }

  return (
    <div className="max-w-3xl p-6">
      <header>
        <h1 className="text-2xl font-semibold">Tags</h1>
        <p className="mt-1 text-sm text-slate-600">
          Labels you can stick on tickets to group them by topic, customer
          tier, or escalation status. Filter the inbox by any tag.
        </p>
      </header>

      {error && (
        <div className="mt-6 rounded border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
          {error}
        </div>
      )}

      {isAdmin && (
        <div className="mt-6">
          <CreateTagForm />
        </div>
      )}

      <section className="mt-6 rounded border border-slate-200 bg-white">
        <header className="border-b border-slate-200 px-4 py-2 text-sm font-semibold">
          All tags ({tags.length})
        </header>
        {tags.length === 0 ? (
          <p className="px-4 py-6 text-sm text-slate-500">
            No tags yet.{isAdmin ? " Add one above to get started." : ""}
          </p>
        ) : (
          <ul>
            {tags.map((t) => (
              <TagRow key={t.id} t={t} canDelete={isAdmin} />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
