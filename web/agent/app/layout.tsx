import "./globals.css";
import type { Metadata } from "next";
import type { ReactNode } from "react";

// Module-level static metadata -- per server-hoist-static-io there's
// nothing dynamic in here, so it lives outside any component body.
export const metadata: Metadata = {
  title: "Contact Centre",
  description: "Agent workspace",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen">{children}</body>
    </html>
  );
}
