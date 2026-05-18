import type { Tag } from "@/lib/types";

// TagChip renders a single tag swatch with its name. Stays a server
// component (no interactivity) so it ships zero JS on the inbox.
// Color is a 6-char RGB hex stored without "#"; if missing we fall
// back to a neutral slate.
export function TagChip({
  tag,
  onRemove,
  size = "sm",
}: {
  tag: Tag;
  onRemove?: () => void;
  size?: "sm" | "md";
}) {
  const fill = tag.color ? `#${tag.color}` : "#94a3b8"; // slate-400
  const pad = size === "md" ? "px-2 py-0.5 text-xs" : "px-1.5 py-0 text-[10px]";
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full font-medium text-white ${pad}`}
      style={{ backgroundColor: fill }}
      title={`#${tag.slug}`}
    >
      {tag.name}
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          aria-label={`Remove ${tag.name}`}
          // Inline ✕ stays the same color as the chip text so it
          // doesn't fight the swatch contrast.
          className="opacity-70 hover:opacity-100"
        >
          ✕
        </button>
      )}
    </span>
  );
}
