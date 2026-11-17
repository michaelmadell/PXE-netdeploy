import { useQuery } from "@tanstack/react-query";
import { Trash2, Upload as UploadIcon } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { api } from "@/api/client";
import { JobProgress } from "@/components/JobProgress";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorMessage } from "@/lib/problem";
import { uploadsActive, useUploads } from "@/lib/tus";
import { DeleteUploadDialog, UploadRow, UploadState } from "@/components/Uploads";
import {
  describeKind,
  importUpload,
  isDriverArchive,
  nameFromFile,
  newKey,
  refreshImages,
  uploadAndImport,
  useImages,
  useImportJobs,
  useUploadList,
  type Image,
  type Upload,
} from "./data";

export function ImagesPage() {
  const images = useImages();
  const jobs = useImportJobs();
  const uploads = useUploadList();
  const tasks = useUploads().filter((t) => t.purpose === "image");
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const { data, error } = await api.GET("/system/health");
      return data ?? (error as NonNullable<typeof data>);
    },
  });

  // `true`: choose a file. An Upload: import that finished upload.
  const [dialog, setDialog] = useState<true | Upload | null>(null);
  const [deleting, setDeleting] = useState<Upload | null>(null);

  // Leaving the page (not the route) would stop an upload part-way.
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (uploadsActive()) e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, []);

  const items = images.data?.items ?? [];
  const jobFor = (im: Image) => jobs.data?.items.find((j) => j.resourceId === im.id);
  // Imports whose image isn't listed yet (queued, or about to appear).
  const orphanJobs = (jobs.data?.items ?? []).filter((j) => !items.some((im) => im.id === j.resourceId));
  // Uploads this page is sending are shown as tasks, not as server rows.
  const sending = new Set(tasks.map((t) => t.uploadId).filter(Boolean));
  const otherUploads = (uploads.data?.items ?? []).filter((u) => !sending.has(u.id) && !isDriverArchive(u.filename));
  const storage = health.data?.storage;

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Images"
        description="Operating system media. Upload a Windows ISO; its editions and boot managers are read during import, and boot images and profiles are built from it."
        actions={
          <Button variant="primary" onClick={() => setDialog(true)}>
            <UploadIcon aria-hidden /> Upload ISO
          </Button>
        }
      />

      {(tasks.length > 0 || orphanJobs.length > 0) && (
        <Section title="In progress" className="mb-6">
          <ul className="space-y-5">
            {tasks.map((t) => (
              <UploadRow key={t.key} task={t} onChanged={refreshImages} />
            ))}
            {orphanJobs.map((j) => (
              <li key={j.id}>
                <JobProgress job={j} what="Import" queuedText="Waiting for another import to finish" />
              </li>
            ))}
          </ul>
        </Section>
      )}

      {images.isPending ? (
        <p className="text-muted-foreground">Loading images…</p>
      ) : images.isError ? (
        <Alert tone="error" title="Images couldn’t be loaded">
          {errorMessage(images.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No images yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Upload a Windows 11 ISO to start. Large files upload in the background and resume if the connection drops.
          </p>
          <Button variant="primary" className="mt-4" onClick={() => setDialog(true)}>
            <UploadIcon aria-hidden /> Upload ISO
          </Button>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Type</th>
                <th scope="col" className={th}>Editions</th>
                <th scope="col" className={th}>Size</th>
                <th scope="col" className={th}>Added</th>
                <th scope="col" className={th}>Status</th>
              </tr>
            </thead>
            <tbody>
              {items.map((im) => {
                const job = jobFor(im);
                return (
                  <tr key={im.id} className="border-b last:border-0">
                    <td className={td}>
                      <Link to={`/images/${im.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                        {im.name}
                      </Link>
                      {im.sourceFilename && <div className="text-xs text-muted-foreground">{im.sourceFilename}</div>}
                    </td>
                    <td className={td}>{describeKind(im)}</td>
                    <td className={`${td} tabular-nums`}>{im.details?.kind === "windows" ? im.details.editions.length : "–"}</td>
                    <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(im.sizeBytes)}</td>
                    <td className={`${td} whitespace-nowrap`}>{formatDateTime(im.createdAt)}</td>
                    <td className={`${td} min-w-44`}>
                      {im.status === "ready" ? (
                        <Badge tone={im.warnings?.length ? "warning" : "success"}>
                          {im.warnings?.length ? "Ready, with warnings" : "Ready"}
                        </Badge>
                      ) : im.status === "failed" ? (
                        <>
                          <Badge tone="error">Import failed</Badge>
                          {im.error?.detail && <p className="mt-1 text-xs text-muted-foreground">{im.error.detail}</p>}
                        </>
                      ) : job ? (
                        <JobProgress job={job} what="Import" queuedText="Waiting for another import to finish" compact />
                      ) : (
                        <Badge>Importing</Badge>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {images.data?.nextCursor && (
            <p className="border-t py-2 text-sm text-muted-foreground">Showing the first 200 images.</p>
          )}
        </div>
      )}

      {otherUploads.length > 0 && (
        <Section
          title="Uploads"
          className="mt-6"
          description="Files held on the server. An image doesn’t need its upload once the import has finished, so imported uploads can be deleted to free space."
        >
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b">
                  <th scope="col" className={th}>File</th>
                  <th scope="col" className={th}>Size</th>
                  <th scope="col" className={th}>State</th>
                  <th scope="col" className={th}>Kept until</th>
                  <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {otherUploads.map((u) => (
                  <tr key={u.id} className="border-b last:border-0">
                    <td className={`${td} break-all`}>{u.filename || <span className="text-muted-foreground">Unnamed</span>}</td>
                    <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(u.length)}</td>
                    <td className={td}>
                      <UploadState upload={u} />
                    </td>
                    <td className={`${td} whitespace-nowrap`}>{u.expiresAt ? formatDateTime(u.expiresAt) : "Until you delete it"}</td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-2">
                        {u.status === "complete" && !u.inUse && !u.imported && (
                          <Button size="sm" aria-label={`Import ${u.filename ?? "upload"}`} onClick={() => setDialog(u)}>
                            Import
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="destructive-ghost"
                          disabled={u.inUse}
                          aria-label={`Delete ${u.filename ?? "upload"}`}
                          title={u.inUse ? "An import is using this upload" : undefined}
                          onClick={() => setDeleting(u)}
                        >
                          <Trash2 aria-hidden /> Delete
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>
      )}

      {storage && (
        <p className="mt-6 text-sm text-muted-foreground">
          {formatBytes(storage.freeBytes)} free of {formatBytes(storage.totalBytes)}. Stored files use{" "}
          {formatBytes(storage.contentBytes)}, uploads {formatBytes(storage.uploadBytes)};{" "}
          {formatBytes(storage.reserveBytes)} is always kept free.
        </p>
      )}

      <ImportDialog target={dialog} onClose={() => setDialog(null)} />
      <DeleteUploadDialog upload={deleting} onClose={() => setDeleting(null)} onDeleted={refreshImages} what="image" />
    </div>
  );
}

/** Choose a file (or take a finished upload), name the image, start. */
function ImportDialog({ target, onClose }: { target: true | Upload | null; onClose: () => void }) {
  const existing = target && target !== true ? target : null;
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [retainIso, setRetainIso] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per opening of the dialog, so a second click can't queue a second import.
  const key = useRef("");

  useEffect(() => {
    if (!target) return;
    key.current = newKey();
    setFile(null);
    setName(existing?.filename ? nameFromFile(existing.filename) : "");
    setRetainIso(false);
    setProblem(null);
    setBusy(false);
  }, [target]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const opts = { name: name.trim(), retainIso };
    if (!opts.name) return setProblem("Give the image a name.");
    if (existing) {
      setBusy(true);
      try {
        await importUpload(existing.id, opts, key.current);
        onClose();
      } catch (err) {
        key.current = newKey(); // a refused request is replayed for its key
        setProblem(errorMessage(err));
        setBusy(false);
      }
      return;
    }
    if (!file) return setProblem("Choose an ISO file to upload.");
    uploadAndImport(file, opts);
    onClose();
  };

  return (
    <Dialog open={!!target} onClose={onClose} title={existing ? "Import upload" : "Upload ISO"}>
      <form id="import-form" className="space-y-5" onSubmit={(e) => void submit(e)} noValidate>
        {existing ? (
          <p className="text-sm">
            <span className="font-medium break-all">{existing.filename || "Unnamed upload"}</span>
            <span className="text-muted-foreground"> · {formatBytes(existing.length)}</span>
          </p>
        ) : (
          <Field label="ISO file" hint="Windows installation media (UDF), up to 64 GiB. If you uploaded part of this file before, it continues from there.">
            {(p) => (
              <input
                {...p}
                type="file"
                accept=".iso,application/x-iso9660-image"
                className="block w-full text-sm file:mr-3 file:h-9 file:rounded-md file:border file:border-input file:bg-card file:px-3 file:text-sm file:font-medium"
                onChange={(e) => {
                  const f = e.target.files?.[0] ?? null;
                  setFile(f);
                  setProblem(null);
                  if (f && !name.trim()) setName(nameFromFile(f.name));
                }}
              />
            )}
          </Field>
        )}
        <Field label="Name" hint="Shown wherever you pick an image, for example “Windows 11 24H2 en-GB”.">
          {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Checkbox
          label="Keep the original ISO"
          hint="Not needed for deployments. Only for features that serve the whole ISO."
          checked={retainIso}
          onChange={setRetainIso}
        />
        {problem && <Alert tone="error">{problem}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {existing ? (busy ? "Starting…" : "Import") : "Upload and import"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
