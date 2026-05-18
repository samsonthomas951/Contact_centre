"use client";

import { useMemo, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import {
  sendMessage,
  uploadAttachment,
  runMacroActions,
  type UploadedDoc,
  type CannedReply,
  type CannedReplyAction,
  type MacroResult,
} from "@/app/(app)/tickets/[id]/actions";

// Client component for the reply box. The actual POST is a server
// action so the bearer token never leaves the server. Attachments
// upload to /v1/documents via uploadAttachment first; the returned
// doc UUIDs ride on the subsequent sendMessage so messages.attachments
// stores the linkage.
//
// Saved replies: typing "/" at the start of the textarea (or after a
// whitespace boundary) opens a picker filtered by the partial shortcut
// the agent's typing. Arrow keys navigate; Enter inserts; Esc closes.

interface Pending {
  // Local-only key so React reconciles the chip during the upload
  // window; replaced by the server-side id once the upload resolves.
  key: string;
  name: string;
  size: number;
  uploading: boolean;
  error?: string;
  doc?: UploadedDoc;
}

// Match a "/word" trigger immediately before the cursor. We allow
// lower-snake-case (matching shortcutPattern in internal/cannedreply
// repo.go). The trigger must follow a whitespace/newline OR the
// beginning of the buffer so URLs like example.com/foo don't open it.
const TRIGGER_RE = /(?:^|\s)\/([a-z][a-z0-9_]{0,31})$/i;

export function Composer({
  ticketId,
  cannedReplies,
  meId,
}: {
  ticketId: string;
  cannedReplies: CannedReply[];
  meId: string;
}) {
  const [body, setBody] = useState("");
  const [direction, setDirection] = useState<"out" | "note">("out");
  const [pending, startTransition] = useTransition();
  const [attachments, setAttachments] = useState<Pending[]>([]);
  // Pending macro: when the inserted reply carries actions, they fire
  // after the message send succeeds. Cleared after each submit.
  const pendingActions = useRef<CannedReplyAction[]>([]);
  // Transient toast for macro outcomes ("macro: 2 ran, 1 failed").
  // Cleared after 4s so the agent has time to notice partial fails
  // without it lingering.
  const [macroToast, setMacroToast] = useState<{ msg: string; bad: boolean } | null>(null);
  const macroTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const router = useRouter();

  const showMacroToast = (msg: string, bad: boolean) => {
    setMacroToast({ msg, bad });
    if (macroTimer.current) clearTimeout(macroTimer.current);
    macroTimer.current = setTimeout(() => setMacroToast(null), 4000);
  };

  // Slash-picker state. pickerQuery is the partial shortcut after
  // "/"; pickerIndex is the highlighted row; triggerStart is the
  // textarea offset of the "/" itself so insertion can replace the
  // exact range.
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerQuery, setPickerQuery] = useState("");
  const [pickerIndex, setPickerIndex] = useState(0);
  const [triggerStart, setTriggerStart] = useState(-1);

  // Filter every render -- cannedReplies is small enough (<<100 rows)
  // that a useMemo is overkill, but the dependency keeps the linter
  // honest. Match against shortcut OR title for forgiving prefix.
  const pickerHits = useMemo(() => {
    if (!pickerOpen) return [];
    const q = pickerQuery.toLowerCase();
    const ranked = cannedReplies.filter(
      (r) => r.shortcut.startsWith(q) || r.title.toLowerCase().includes(q),
    );
    return ranked.slice(0, 8);
  }, [cannedReplies, pickerOpen, pickerQuery]);

  // detectTrigger inspects the body + caret position to decide if a
  // "/foo" partial is being typed. Called from onChange so the picker
  // reacts to every keystroke, not just "/" itself (the agent may
  // also paste or arrow into a /foo region).
  const detectTrigger = (text: string, caret: number) => {
    const head = text.slice(0, caret);
    const m = TRIGGER_RE.exec(head);
    if (!m) {
      if (pickerOpen) setPickerOpen(false);
      return;
    }
    const slash = head.lastIndexOf("/", caret);
    setTriggerStart(slash);
    setPickerQuery(m[1]);
    setPickerIndex(0);
    setPickerOpen(true);
  };

  const insertReply = (r: CannedReply) => {
    if (triggerStart < 0 || !textareaRef.current) return;
    const ta = textareaRef.current;
    const caret = ta.selectionStart;
    const before = body.slice(0, triggerStart);
    const after = body.slice(caret);
    const next = before + r.body + after;
    setBody(next);
    setPickerOpen(false);
    // If this reply is a macro (carries actions), stash them so they
    // fire after the next successful send. Single-step inserts also
    // overwrite any previously stashed actions -- only the most
    // recently picked macro applies, matching agent intent.
    pendingActions.current = r.actions ?? [];
    // Move the caret to the end of the inserted body on the next tick
    // so React has applied the new value.
    queueMicrotask(() => {
      if (!textareaRef.current) return;
      const pos = before.length + r.body.length;
      textareaRef.current.focus();
      textareaRef.current.setSelectionRange(pos, pos);
    });
  };

  const submit = () => {
    const trimmed = body.trim();
    const uploaded = attachments.filter((a) => a.doc).map((a) => a.doc!.id);
    if (!trimmed && uploaded.length === 0) return;
    if (attachments.some((a) => a.uploading)) return; // wait for uploads

    setBody("");
    setAttachments([]);
    const actions = pendingActions.current;
    pendingActions.current = [];
    startTransition(async () => {
      try {
        await sendMessage(ticketId, {
          direction,
          body: trimmed || "(attachment)",
          attachments: uploaded,
        });
        // Macro side-effects fire after the message lands. We do
        // not refresh between them -- the final revalidatePath
        // inside runMacroActions handles it. Toast surfaces any
        // step failures so the agent isn't left guessing whether
        // the macro actually ran.
        let macroResult: MacroResult | null = null;
        if (actions.length > 0) {
          macroResult = await runMacroActions(ticketId, actions, meId);
        }
        router.refresh();
        if (macroResult) {
          if (macroResult.failed.length === 0) {
            showMacroToast(
              `macro: ${macroResult.ran} step${macroResult.ran === 1 ? "" : "s"} ran`,
              false,
            );
          } else {
            const reasons = macroResult.failed
              .map((f) => `${f.type}: ${f.reason}`)
              .join("; ");
            showMacroToast(
              `macro: ${macroResult.ran} ran, ${macroResult.failed.length} failed — ${reasons}`,
              true,
            );
          }
        }
      } catch (e) {
        setBody(trimmed); // restore so the agent can retry
        // eslint-disable-next-line no-console
        console.error("send failed", e);
      }
    });
  };

  // onFiles is called from the hidden <input>. We upload each file
  // serially (sequential keeps it simple and avoids hammering the
  // gateway when an agent picks 10 PDFs); each chip is in the DOM
  // immediately so the agent sees progress.
  const onFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    const seed: Pending[] = Array.from(files).map((f) => ({
      key: `${f.name}-${f.lastModified}-${Math.random()}`,
      name: f.name,
      size: f.size,
      uploading: true,
    }));
    setAttachments((prev) => [...prev, ...seed]);

    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      const k = seed[i].key;
      try {
        const form = new FormData();
        form.append("file", f);
        const doc = await uploadAttachment(ticketId, form);
        setAttachments((prev) =>
          prev.map((a) => (a.key === k ? { ...a, uploading: false, doc } : a)),
        );
      } catch (e) {
        const msg = e instanceof Error ? e.message : "upload failed";
        setAttachments((prev) =>
          prev.map((a) => (a.key === k ? { ...a, uploading: false, error: msg } : a)),
        );
      }
    }
    // Reset the file input so picking the same file again re-fires onChange.
    if (fileInput.current) fileInput.current.value = "";
  };

  const sendDisabled =
    pending ||
    attachments.some((a) => a.uploading) ||
    (body.trim() === "" && attachments.filter((a) => a.doc).length === 0);

  return (
    <form
      className="border-t border-slate-200 bg-white p-3"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="mb-2 flex items-center gap-2 text-xs">
        <DirectionToggle value={direction} onChange={setDirection} />
        {macroToast && (
          <span
            className={
              "ml-auto truncate rounded px-2 py-0.5 text-[11px] " +
              (macroToast.bad
                ? "bg-red-100 text-red-800"
                : "bg-emerald-100 text-emerald-800")
            }
            title={macroToast.msg}
          >
            {macroToast.msg}
          </span>
        )}
      </div>

      {attachments.length > 0 && (
        <ul className="mb-2 flex flex-wrap gap-1.5 text-xs">
          {attachments.map((a) => (
            <AttachmentChip
              key={a.key}
              a={a}
              onRemove={() =>
                setAttachments((prev) => prev.filter((x) => x.key !== a.key))
              }
            />
          ))}
        </ul>
      )}

      <div className="flex items-end gap-2">
        <button
          type="button"
          onClick={() => fileInput.current?.click()}
          title="Attach file"
          className="rounded border border-slate-300 px-2 py-2 text-sm hover:bg-slate-100"
        >
          📎
        </button>
        <input
          ref={fileInput}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => onFiles(e.target.files)}
        />
        <div className="relative flex-1">
          {pickerOpen && pickerHits.length > 0 && (
            <SlashPicker
              hits={pickerHits}
              activeIndex={pickerIndex}
              onPick={insertReply}
            />
          )}
          <textarea
            ref={textareaRef}
            // Stable selector so the ticket-page keyboard shortcut
            // ("r" focuses reply) can find this textarea.
            data-composer-textarea
            value={body}
            onChange={(e) => {
              setBody(e.target.value);
              detectTrigger(e.target.value, e.target.selectionStart);
            }}
            onKeyDown={(e) => {
              if (pickerOpen && pickerHits.length > 0) {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setPickerIndex((i) => (i + 1) % pickerHits.length);
                  return;
                }
                if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setPickerIndex((i) => (i - 1 + pickerHits.length) % pickerHits.length);
                  return;
                }
                if (e.key === "Enter" && !(e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  insertReply(pickerHits[pickerIndex]);
                  return;
                }
                if (e.key === "Escape" || e.key === "Tab") {
                  e.preventDefault();
                  setPickerOpen(false);
                  return;
                }
              }
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                e.preventDefault();
                submit();
              }
            }}
            rows={2}
            placeholder={direction === "note" ? "Internal note..." : "Reply to customer... (try / for saved replies)"}
            className="w-full resize-none rounded border border-slate-300 px-3 py-2 text-sm focus:border-brand focus:outline-none"
            disabled={pending}
          />
        </div>
        <button
          type="submit"
          disabled={sendDisabled}
          className="rounded bg-brand px-4 py-2 text-sm font-semibold text-white disabled:opacity-50"
        >
          {pending ? "Sending..." : "Send"}
        </button>
      </div>
    </form>
  );
}

// Module-scope per rerender-no-inline-components.
function SlashPicker({
  hits,
  activeIndex,
  onPick,
}: {
  hits: CannedReply[];
  activeIndex: number;
  onPick: (r: CannedReply) => void;
}) {
  return (
    <ul
      // Position above the textarea so the picker grows upward (the
      // composer is anchored at the bottom of the page).
      className="absolute bottom-full left-0 right-0 mb-1 max-h-64 overflow-y-auto rounded border border-slate-200 bg-white text-sm shadow-lg"
      role="listbox"
    >
      {hits.map((r, i) => (
        <li
          key={r.id}
          role="option"
          aria-selected={i === activeIndex}
          // onMouseDown rather than onClick so the textarea doesn't
          // lose focus (and the picker doesn't close) before the
          // insertion runs.
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(r);
          }}
          className={
            "cursor-pointer px-3 py-1.5 " +
            (i === activeIndex
              ? "bg-brand text-white"
              : "text-slate-800 hover:bg-slate-50")
          }
        >
          <div className="flex items-baseline gap-2">
            <code className={i === activeIndex ? "text-white" : "text-slate-500"}>/{r.shortcut}</code>
            <span className="font-medium">{r.title}</span>
            {r.actions && r.actions.length > 0 && (
              <span
                className={
                  "rounded-full px-1.5 py-0 text-[9px] uppercase " +
                  (i === activeIndex
                    ? "bg-white/20 text-white"
                    : "bg-purple-100 text-purple-700")
                }
                title={macroSummary(r.actions)}
              >
                macro
              </span>
            )}
            {!r.owner_agent_id && (
              <span className={"ml-auto text-[10px] " + (i === activeIndex ? "text-blue-100" : "text-slate-400")}>
                shared
              </span>
            )}
          </div>
          <div className={"truncate text-xs " + (i === activeIndex ? "text-blue-100" : "text-slate-500")}>
            {r.body.split("\n")[0]}
          </div>
        </li>
      ))}
    </ul>
  );
}

// macroSummary builds a human hint for the picker tooltip.
function macroSummary(actions: CannedReplyAction[]): string {
  return actions
    .map((a) => {
      if (a.type === "set_state") return `state→${a.to}`;
      if (a.type === "add_tag") return `+#${a.tag_slug}`;
      if (a.type === "assign") return `assign→${a.to}`;
      return a.type;
    })
    .join(", ");
}

function AttachmentChip({ a, onRemove }: { a: Pending; onRemove: () => void }) {
  return (
    <li
      className={
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 " +
        (a.error
          ? "border-red-200 bg-red-50 text-red-700"
          : a.uploading
          ? "border-slate-200 bg-slate-50 text-slate-500"
          : "border-emerald-200 bg-emerald-50 text-emerald-800")
      }
      title={a.error ?? a.name}
    >
      <span className="max-w-[12rem] truncate">{a.name}</span>
      <span className="text-[10px] text-slate-500">{prettyBytes(a.size)}</span>
      {a.uploading ? <span className="text-[10px]">…</span> : null}
      {a.error ? <span className="text-[10px]">!</span> : null}
      <button
        type="button"
        onClick={onRemove}
        className="ml-1 text-slate-400 hover:text-slate-700"
        aria-label="Remove attachment"
      >
        ✕
      </button>
    </li>
  );
}

function prettyBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function DirectionToggle({
  value,
  onChange,
}: {
  value: "out" | "note";
  onChange: (v: "out" | "note") => void;
}) {
  return (
    <>
      <ToggleBtn active={value === "out"} onClick={() => onChange("out")}>Reply</ToggleBtn>
      <ToggleBtn active={value === "note"} onClick={() => onChange("note")}>Internal note</ToggleBtn>
    </>
  );
}

function ToggleBtn({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-full px-2 py-0.5 " +
        (active ? "bg-slate-900 text-white" : "bg-slate-100 text-slate-700 hover:bg-slate-200")
      }
    >
      {children}
    </button>
  );
}
