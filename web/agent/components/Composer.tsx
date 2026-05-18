"use client";

import { useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { sendMessage, uploadAttachment, type UploadedDoc } from "@/app/(app)/tickets/[id]/actions";

// Client component for the reply box. The actual POST is a server
// action so the bearer token never leaves the server. Attachments
// upload to /v1/documents via uploadAttachment first; the returned
// doc UUIDs ride on the subsequent sendMessage so messages.attachments
// stores the linkage.

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

export function Composer({ ticketId }: { ticketId: string }) {
  const [body, setBody] = useState("");
  const [direction, setDirection] = useState<"out" | "note">("out");
  const [pending, startTransition] = useTransition();
  const [attachments, setAttachments] = useState<Pending[]>([]);
  const fileInput = useRef<HTMLInputElement>(null);
  const router = useRouter();

  const submit = () => {
    const trimmed = body.trim();
    const uploaded = attachments.filter((a) => a.doc).map((a) => a.doc!.id);
    if (!trimmed && uploaded.length === 0) return;
    if (attachments.some((a) => a.uploading)) return; // wait for uploads

    setBody("");
    setAttachments([]);
    startTransition(async () => {
      try {
        await sendMessage(ticketId, {
          direction,
          body: trimmed || "(attachment)",
          attachments: uploaded,
        });
        router.refresh();
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
      <div className="mb-2 flex gap-2 text-xs">
        <DirectionToggle value={direction} onChange={setDirection} />
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
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
          rows={2}
          placeholder={direction === "note" ? "Internal note..." : "Reply to customer..."}
          className="flex-1 resize-none rounded border border-slate-300 px-3 py-2 text-sm focus:border-brand focus:outline-none"
          disabled={pending}
        />
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
