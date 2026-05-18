"use client";

import { useState, useEffect } from "react";
import { usePathname } from "next/navigation";

// MobileNav owns the hamburger button + slide-in drawer state.
// On desktop (md+) the wrapper renders nothing; the static sidebar
// in the layout handles navigation. On mobile we hide the static
// sidebar and let this component overlay a drawer when toggled.
//
// The drawer markup itself is passed as children so the nav links
// stay in the layout file (server component) -- this client island
// only owns the open/close state.
export function MobileNav({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const pathname = usePathname();

  // Close when the route changes so the drawer doesn't linger on
  // top of the new page.
  useEffect(() => {
    setOpen(false);
  }, [pathname]);

  // Lock body scroll while the drawer is open; restore on close.
  useEffect(() => {
    if (open) {
      const prev = document.body.style.overflow;
      document.body.style.overflow = "hidden";
      return () => {
        document.body.style.overflow = prev;
      };
    }
  }, [open]);

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label="Open menu"
        className="inline-flex h-9 w-9 items-center justify-center rounded border border-slate-300 bg-white text-slate-700 md:hidden"
      >
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M4 6h16M4 12h16M4 18h16" />
        </svg>
      </button>
      {open && (
        <div className="fixed inset-0 z-50 md:hidden">
          <div
            className="absolute inset-0 bg-slate-900/40"
            onClick={() => setOpen(false)}
            aria-hidden="true"
          />
          <div className="absolute left-0 top-0 h-full w-64 max-w-[80vw] overflow-y-auto bg-white shadow-xl">
            <div className="flex items-center justify-between border-b border-slate-200 p-3">
              <span className="text-sm font-semibold">Menu</span>
              <button
                type="button"
                onClick={() => setOpen(false)}
                aria-label="Close menu"
                className="rounded p-1 text-slate-500 hover:bg-slate-100"
              >
                ✕
              </button>
            </div>
            <div className="p-3">{children}</div>
          </div>
        </div>
      )}
    </>
  );
}
