import { useMutation } from "@tanstack/react-query";
import { Pause, Play, RotateCw, X } from "lucide-react";
import { useEffect } from "react";
import type { Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Alert, Badge, Dialog, Progress } from "@/components/ui/misc";
import { formatBytes, formatDuration } from "@/lib/format";
import { errorMessage } from "@/lib/problem";
import { deleteUpload, pauseUpload, removeUpload, resumeUpload, type UploadTask } from "@/lib/tus";

// Shared by the Images and Driver packages pages.

type Upload = Schemas["Upload"];

export function UploadState({ upload: u }: { upload: Upload }) {
  if (u.status === "receiving") {
    const pct = u.length ? Math.floor((u.offset / u.length) * 100) : 0;
    return (
      <>
        <Badge tone="warning">Incomplete, {pct}% received</Badge>
        <p className="mt-1 text-xs text-muted-foreground">Upload the same file again to continue from here.</p>
      </>
    );
  }
  if (u.inUse) return <Badge>Importing</Badge>;
  if (u.imported) return <Badge tone="success">Imported</Badge>;
  return <Badge>Ready to import</Badge>;
}

/** An upload this page is sending: progress, pause, resume, cancel. `onChanged` runs after it is removed. */
export function UploadRow({ task: t, onChanged }: { task: UploadTask; onChanged: () => void }) {
  const pct = t.totalBytes ? (t.sentBytes / t.totalBytes) * 100 : 0;
  const uploaded = !!t.uploadId && t.sentBytes >= t.totalBytes;
  const left = t.bytesPerSecond > 0 ? (t.totalBytes - t.sentBytes) / t.bytesPerSecond : NaN;
  const status =
    t.status === "starting"
      ? "Starting…"
      : t.status === "uploading"
        ? `${formatBytes(t.sentBytes)} of ${formatBytes(t.totalBytes)}` +
          (t.bytesPerSecond > 0 ? ` · ${formatBytes(t.bytesPerSecond)}/s · ${formatDuration(left)} left` : "")
        : t.status === "paused"
          ? `Paused at ${formatBytes(t.sentBytes)} of ${formatBytes(t.totalBytes)}`
          : t.status === "complete"
            ? "Uploaded. Starting the import…"
            : "";
  return (
    <li>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{t.file.name}</p>
          <p className="text-xs text-muted-foreground tabular-nums" aria-live="off">
            {status}
            {t.resumed && t.status === "uploading" && " · continued an earlier upload"}
          </p>
        </div>
        <div className="flex gap-2">
          {(t.status === "uploading" || t.status === "starting") && (
            <Button size="sm" onClick={() => pauseUpload(t.key)}>
              <Pause aria-hidden /> Pause
            </Button>
          )}
          {t.status === "paused" && (
            <Button size="sm" onClick={() => resumeUpload(t.key)}>
              <Play aria-hidden /> Resume
            </Button>
          )}
          {t.status === "failed" && (
            <Button size="sm" onClick={() => resumeUpload(t.key)}>
              <RotateCw aria-hidden /> Try again
            </Button>
          )}
          {t.status !== "complete" &&
            (uploaded ? (
              // The file is on the server: keep it there, listed under Uploads.
              <Button size="sm" variant="ghost" onClick={() => void removeUpload(t.key, { keep: true }).then(onChanged)}>
                <X aria-hidden /> Dismiss
              </Button>
            ) : (
              <Button size="sm" variant="destructive-ghost" onClick={() => void removeUpload(t.key).then(onChanged)}>
                <X aria-hidden /> Cancel
              </Button>
            ))}
        </div>
      </div>
      <Progress className="mt-2" value={pct} label={`Uploading ${t.file.name}`} />
      {t.error && (
        <p role="alert" className="mt-1.5 text-sm text-destructive">
          {t.error}
        </p>
      )}
    </li>
  );
}

/** Confirms and deletes a server upload. `what` names what is imported from such uploads. */
export function DeleteUploadDialog({
  upload,
  onClose,
  onDeleted,
  what,
}: {
  upload: Upload | null;
  onClose: () => void;
  onDeleted: () => void;
  what: string;
}) {
  const del = useMutation({
    mutationFn: (id: string) => deleteUpload(id),
    onSuccess: () => {
      onDeleted();
      onClose();
    },
  });
  useEffect(() => {
    if (upload) del.reset();
  }, [upload]);
  return (
    <Dialog
      open={!!upload}
      onClose={onClose}
      title="Delete upload?"
      footer={
        <>
          <Button onClick={onClose}>Keep</Button>
          <Button variant="destructive" disabled={del.isPending} onClick={() => upload && del.mutate(upload.id)}>
            {del.isPending ? "Deleting…" : "Delete upload"}
          </Button>
        </>
      }
    >
      <p className="text-sm">
        <span className="font-medium break-all">{upload?.filename || "This upload"}</span> ({formatBytes(upload?.length)}) will
        be removed from the server.{" "}
        {upload?.imported
          ? `The ${what} imported from it is not affected.`
          : "It has not been imported, so you would need to upload the file again."}
      </p>
      {del.isError && (
        <Alert tone="error" className="mt-4">
          {errorMessage(del.error)}
        </Alert>
      )}
    </Dialog>
  );
}
