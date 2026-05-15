// Pure helpers used in both server and client components. No imports
// of "server-only" so they stay isomorphic.

const RELATIVE = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

// formatRelative produces "5 min ago", "2 days ago", etc. -- accepts
// an ISO string or a Date.
export function formatRelative(iso: string | Date | undefined): string {
  if (!iso) return "";
  const t = typeof iso === "string" ? new Date(iso) : iso;
  const diffMs = t.getTime() - Date.now();
  const abs = Math.abs(diffMs);
  if (abs < 60_000) return "just now";
  if (abs < 3_600_000) return RELATIVE.format(Math.round(diffMs / 60_000), "minute");
  if (abs < 86_400_000) return RELATIVE.format(Math.round(diffMs / 3_600_000), "hour");
  return RELATIVE.format(Math.round(diffMs / 86_400_000), "day");
}

// priorityLabel maps 1..5 to the human label used in the badge.
export function priorityLabel(p: number): string {
  return ({ 1: "P1 urgent", 2: "P2 high", 3: "P3 normal", 4: "P4 low", 5: "P5 lowest" } as Record<number, string>)[p] ?? `P${p}`;
}

// Channel dot indicator colour — keeps the inbox row scannable.
export function channelColor(c: string): string {
  switch (c) {
    case "fb": return "bg-blue-600";
    case "x": return "bg-slate-900";
    case "wa": return "bg-green-600";
    case "ig": return "bg-pink-600";
    case "widget": return "bg-amber-500";
    case "voice": return "bg-violet-600";
    default: return "bg-slate-400";
  }
}
