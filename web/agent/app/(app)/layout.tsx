import type { ReactNode } from "react";
import Link from "next/link";
import { auth, signOut } from "@/lib/auth";
import { redirect } from "next/navigation";
import { RealtimeRefresher } from "@/components/RealtimeRefresher";
import { MobileNav } from "@/components/MobileNav";

// Auth-guarded shell. The middleware also redirects unauthenticated
// requests, but checking here is the defense-in-depth layer per the
// gateway's RBAC posture: the page never renders without a session.
//
// Responsive layout:
//   md+   static 16rem sidebar + main column
//   <md   hamburger top bar + slide-in drawer (see MobileNav)
export default async function AppLayout({ children }: { children: ReactNode }) {
  const session = await auth();
  if (!session?.accessToken) {
    redirect("/api/auth/signin");
  }

  const navLinks = <NavLinks />;
  const signOutForm = (
    <form
      action={async () => {
        "use server";
        await signOut({ redirectTo: "/api/auth/signin" });
      }}
      className="mt-6"
    >
      <button
        type="submit"
        className="w-full rounded border border-slate-300 px-3 py-1.5 text-xs text-slate-600 hover:bg-slate-100"
      >
        Sign out
      </button>
    </form>
  );

  return (
    <div className="min-h-screen md:grid md:grid-cols-[16rem_1fr]">
      {/* Static sidebar -- hidden on mobile. */}
      <aside className="hidden border-r border-slate-200 bg-white p-4 md:block">
        <div className="mb-6">
          <div className="text-lg font-semibold">Contact Centre</div>
          <div className="text-xs text-slate-500">{session.user?.email}</div>
        </div>
        {navLinks}
        {signOutForm}
      </aside>

      {/* Mobile top bar -- shown only on mobile. */}
      <header className="flex items-center justify-between border-b border-slate-200 bg-white px-3 py-2 md:hidden">
        <MobileNav>
          <div className="mb-4">
            <div className="text-base font-semibold">Contact Centre</div>
            <div className="text-xs text-slate-500">{session.user?.email}</div>
          </div>
          {navLinks}
          {signOutForm}
        </MobileNav>
        <span className="text-sm font-semibold">Contact Centre</span>
        <span className="w-9" aria-hidden="true" />
      </header>

      <main className="overflow-x-hidden">{children}</main>
      {/* One subscriber per app load; triggers router.refresh() on
          any inbound WS frame so RSCs re-render with fresh data. */}
      <RealtimeRefresher wsURL={`${process.env.NEXT_PUBLIC_WS_URL ?? ""}/ws/agent`} />
    </div>
  );
}

// Defined at module scope (not inside AppLayout) per
// rerender-no-inline-components -- otherwise React rebuilds the
// component identity on every render and the link re-mounts.
function NavLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link
      href={href}
      className="rounded px-3 py-1.5 text-slate-700 hover:bg-slate-100"
    >
      {children}
    </Link>
  );
}

function NavLinks() {
  return (
    <nav className="flex flex-col gap-1 text-sm">
      <NavLink href="/inbox">Inbox</NavLink>
      <NavLink href="/supervisor">Supervisor</NavLink>
      <NavLink href="/channels">Channels</NavLink>
      <NavLink href="/widgets">Widgets</NavLink>
      <NavLink href="/canned-replies">Saved replies</NavLink>
      <NavLink href="/tags">Tags</NavLink>
    </nav>
  );
}
