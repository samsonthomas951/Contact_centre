import { gatewayJSON, GatewayError } from "@/lib/api";
import { auth } from "@/lib/auth";
import { CreateForm } from "./CreateForm";
import { ReplyRow, type Reply } from "./ReplyRow";

// Saved replies management. Anyone authed can create personal
// replies; admins can additionally create tenant-shared ones (the
// gateway enforces this -- the form just hides the option for the
// rest). Delete is owner-or-admin (also gateway-enforced).

export const dynamic = "force-dynamic";

export default async function CannedRepliesPage() {
  const session = await auth();
  if (!session?.accessToken) return null;

  // Parse the role out of the demo bearer so the form can hide the
  // tenant-shared option for non-admins. In OIDC production the role
  // would come off the JWT claims; for the demo path it's the last
  // colon-separated segment.
  const tok = session.accessToken;
  const role = tok.startsWith("demo:") ? tok.split(":")[3] ?? "" : "";
  const isAdmin = role.split(",").includes("admin");

  let replies: Reply[] = [];
  let error: string | null = null;
  try {
    const res = await gatewayJSON<{ replies: Reply[] }>("/v1/canned-replies/", { cache: "no-store" });
    replies = res.replies ?? [];
  } catch (e) {
    error = e instanceof GatewayError ? e.message : "Failed to load.";
  }

  // Split shared vs personal so the page reads like the picker.
  const shared = replies.filter((r) => !r.owner_agent_id);
  const personal = replies.filter((r) => !!r.owner_agent_id);

  return (
    <div className="max-w-3xl p-6">
      <header>
        <h1 className="text-2xl font-semibold">Saved replies</h1>
        <p className="mt-1 text-sm text-slate-600">
          Snippets the composer surfaces when you type <code>/</code>.
          {isAdmin && " You can publish tenant-shared replies that everyone on the team can use."}
        </p>
      </header>

      {error && (
        <div className="mt-6 rounded border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
          {error}
        </div>
      )}

      <div className="mt-6">
        <CreateForm canShare={isAdmin} />
      </div>

      <Section title="Tenant-shared" empty="No tenant-shared replies yet." rows={shared} canDelete={isAdmin} />
      <Section title="Personal" empty="No personal replies yet." rows={personal} canDelete={true} />
    </div>
  );
}

function Section({
  title,
  empty,
  rows,
  canDelete,
}: {
  title: string;
  empty: string;
  rows: Reply[];
  canDelete: boolean;
}) {
  return (
    <section className="mt-6 rounded border border-slate-200 bg-white">
      <header className="border-b border-slate-200 px-4 py-2 text-sm font-semibold">
        {title} ({rows.length})
      </header>
      {rows.length === 0 ? (
        <p className="px-4 py-6 text-sm text-slate-500">{empty}</p>
      ) : (
        <ul>
          {rows.map((r) => (
            <ReplyRow key={r.id} r={r} canDelete={canDelete} />
          ))}
        </ul>
      )}
    </section>
  );
}
