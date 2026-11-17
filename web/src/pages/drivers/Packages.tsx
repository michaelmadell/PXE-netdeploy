import { Search, Trash2, Upload as UploadIcon, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { JobProgress } from "@/components/JobProgress";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { DeleteUploadDialog, UploadRow, UploadState } from "@/components/Uploads";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorMessage } from "@/lib/problem";
import { uploadsActive, useUploads } from "@/lib/tus";
import { useCapabilities, useDriverGroups } from "@/pages/boot/data";
import { isDriverArchive, newKey, useUploadList, type Upload } from "@/pages/images/data";
import {
  archiveProblem,
  contents,
  driverError,
  groupsWith,
  importDriverUpload,
  packageNameFromFile,
  refreshDrivers,
  signatureTone,
  signingSummary,
  uploadAndImportDriver,
  useDriverImportJobs,
  useDriverPackages,
  type DriverPackage,
} from "./data";

export function DriverPackagesPage() {
  const [hwInput, setHwInput] = useState("");
  const [hwId, setHwId] = useState("");
  const packages = useDriverPackages(hwId);
  const groups = useDriverGroups();
  const jobs = useDriverImportJobs();
  const uploads = useUploadList();
  const tasks = useUploads().filter((t) => t.purpose === "driver");
  const caps = useCapabilities();
  const [dialog, setDialog] = useState<true | Upload | null>(null);
  const [deleting, setDeleting] = useState<Upload | null>(null);

  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (uploadsActive()) e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, []);

  const items = packages.data ?? [];
  const jobFor = (p: DriverPackage) => jobs.data?.items.find((j) => j.resourceId === p.id);
  const orphanJobs = hwId ? [] : (jobs.data?.items ?? []).filter((j) => !items.some((p) => p.id === j.resourceId));
  const sending = new Set(tasks.map((t) => t.uploadId).filter(Boolean));
  const archives = (uploads.data?.items ?? []).filter((u) => !sending.has(u.id) && isDriverArchive(u.filename));
  const noLzx = caps.data?.cabLzx && !caps.data.cabLzx.available;
  const add = (
    <Button variant="primary" onClick={() => setDialog(true)}>
      <UploadIcon aria-hidden /> Add package
    </Button>
  );

  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Driver packages"
        description="Drivers from hardware vendors, imported from a zip, cab or 7z of driver folders. Every INF is read and its signature checked. Put packages in driver groups to use them in boot images and profiles."
        actions={add}
      />

      {noLzx && (
        <Alert tone="info" className="mb-6" title="Compressed CAB files can’t be opened on this server">
          Drivers from the Microsoft Update Catalog are usually LZX-compressed CABs. Install{" "}
          {caps.data?.hostOs === "windows" ? "expand.exe (part of Windows)" : <code className="font-mono">cabextract</code>} to import them, or
          upload the drivers as a zip instead.
        </Alert>
      )}

      {(tasks.length > 0 || orphanJobs.length > 0) && (
        <Section title="In progress" className="mb-6">
          <ul className="space-y-5">
            {tasks.map((t) => (
              <UploadRow key={t.key} task={t} onChanged={refreshDrivers} />
            ))}
            {orphanJobs.map((j) => (
              <li key={j.id}>
                <JobProgress job={j} what="Import" queuedText="Waiting for another import to finish" />
              </li>
            ))}
          </ul>
        </Section>
      )}

      <form
        className="mb-4 flex max-w-2xl gap-2"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          setHwId(hwInput.trim());
        }}
      >
        <Input
          aria-label="Hardware ID"
          placeholder="Find by hardware ID, e.g. PCI\VEN_8086&DEV_1533"
          className="font-mono"
          value={hwInput}
          onChange={(e) => setHwInput(e.target.value)}
        />
        <Button type="submit">
          <Search aria-hidden /> Find
        </Button>
        {hwId && (
          <Button
            variant="ghost"
            onClick={() => {
              setHwId("");
              setHwInput("");
            }}
          >
            <X aria-hidden /> Clear
          </Button>
        )}
      </form>
      {hwId && packages.data && (
        <p className="mb-4 text-sm text-muted-foreground">
          {items.length === 0 ? "No package has a driver for " : `${items.length} package${items.length === 1 ? " has" : "s have"} a driver for `}
          <code className="font-mono">{hwId}</code>.
        </p>
      )}

      {packages.isPending ? (
        <p className="text-muted-foreground">Loading driver packages…</p>
      ) : packages.isError ? (
        <Alert tone="error" title="Driver packages couldn’t be loaded">
          {errorMessage(packages.error)}
        </Alert>
      ) : items.length === 0 && !hwId ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No driver packages yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Most machines need none. Add the network or storage drivers WinPE lacks for your hardware (an Intel I210 network card, a PERC or
            Intel VMD controller), and any drivers Windows should have installed.
          </p>
          <div className="mt-4 flex justify-center">{add}</div>
        </div>
      ) : items.length > 0 ? (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Contains</th>
                <th scope="col" className={th}>Signing</th>
                <th scope="col" className={th}>Groups</th>
                <th scope="col" className={th}>Size</th>
                <th scope="col" className={th}>Status</th>
              </tr>
            </thead>
            <tbody>
              {items.map((p) => {
                const job = jobFor(p);
                const sign = signingSummary(p);
                const inGroups = groupsWith(groups.data?.items, p.id!);
                return (
                  <tr key={p.id} className="border-b last:border-0">
                    <td className={td}>
                      <Link to={`/drivers/${p.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                        {p.name}
                      </Link>
                      {p.sourceFilename && <div className="text-xs text-muted-foreground break-all">{p.sourceFilename}</div>}
                    </td>
                    <td className={td}>{p.status === "ready" ? contents(p) : "–"}</td>
                    <td className={td}>{sign ? <Badge tone={signatureTone[sign.sig]}>{sign.text}</Badge> : "–"}</td>
                    <td className={td}>
                      {inGroups.length ? inGroups.map((g) => g.name).join(", ") : <span className="text-muted-foreground">None</span>}
                    </td>
                    <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(p.sizeBytes)}</td>
                    <td className={`${td} min-w-44`}>
                      {p.status === "ready" ? (
                        <Badge tone={p.warnings?.length ? "warning" : "success"}>{p.warnings?.length ? "Ready, with warnings" : "Ready"}</Badge>
                      ) : p.status === "failed" ? (
                        <>
                          <Badge tone="error">Import failed</Badge>
                          {p.error && <p className="mt-1 text-xs text-muted-foreground">{p.error.detail ?? p.error.title}</p>}
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
        </div>
      ) : null}

      {archives.length > 0 && !hwId && (
        <Section
          title="Uploads"
          className="mt-6"
          description="Driver archives held on the server. A package doesn’t need its upload once imported, so imported uploads can be deleted to free space."
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
                {archives.map((u) => (
                  <tr key={u.id} className="border-b last:border-0">
                    <td className={`${td} break-all`}>{u.filename}</td>
                    <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(u.length)}</td>
                    <td className={td}>
                      <UploadState upload={u} />
                    </td>
                    <td className={`${td} whitespace-nowrap`}>{u.expiresAt ? formatDateTime(u.expiresAt) : "Until you delete it"}</td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-2">
                        {u.status === "complete" && !u.inUse && !u.imported && (
                          <Button size="sm" aria-label={`Import ${u.filename}`} onClick={() => setDialog(u)}>
                            Import
                          </Button>
                        )}
                        <Button size="sm" variant="destructive-ghost" disabled={u.inUse} aria-label={`Delete ${u.filename}`} onClick={() => setDeleting(u)}>
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

      <AddDialog target={dialog} onClose={() => setDialog(null)} />
      <DeleteUploadDialog upload={deleting} onClose={() => setDeleting(null)} onDeleted={refreshDrivers} what="driver package" />
    </div>
  );
}

/** Choose an archive (or take a finished upload), name the package, start. */
function AddDialog({ target, onClose }: { target: true | Upload | null; onClose: () => void }) {
  const existing = target && target !== true ? target : null;
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef("");

  useEffect(() => {
    if (!target) return;
    key.current = newKey();
    setFile(null);
    setName(existing?.filename ? packageNameFromFile(existing.filename) : "");
    setProblem(null);
    setBusy(false);
  }, [target]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return setProblem("Give the package a name.");
    if (existing) {
      setBusy(true);
      try {
        await importDriverUpload(existing.id, n, key.current);
        onClose();
      } catch (err) {
        key.current = newKey();
        setProblem(driverError(err));
        setBusy(false);
      }
      return;
    }
    if (!file) return setProblem("Choose a driver archive to upload.");
    const bad = archiveProblem(file.name);
    if (bad) return setProblem(bad);
    uploadAndImportDriver(file, n);
    onClose();
  };

  return (
    <Dialog open={!!target} onClose={onClose} title={existing ? "Import upload" : "Add driver package"}>
      <form className="space-y-5" onSubmit={(e) => void submit(e)} noValidate>
        {existing ? (
          <p className="text-sm">
            <span className="font-medium break-all">{existing.filename}</span>
            <span className="text-muted-foreground"> · {formatBytes(existing.length)}</span>
          </p>
        ) : (
          <Field
            label="Driver archive"
            hint="A .zip, .cab or .7z of driver folders: each with its .inf, .sys and .cat files. Vendor installers (.exe, .msi) need extracting first."
          >
            {(p) => (
              <input
                {...p}
                type="file"
                accept=".zip,.cab,.7z,application/zip,application/vnd.ms-cab-compressed,application/x-7z-compressed"
                className="block w-full text-sm file:mr-3 file:h-9 file:rounded-md file:border file:border-input file:bg-card file:px-3 file:text-sm file:font-medium"
                onChange={(e) => {
                  const f = e.target.files?.[0] ?? null;
                  setFile(f);
                  const bad = f ? archiveProblem(f.name) : null;
                  setProblem(bad);
                  if (f && !bad && !name.trim()) setName(packageNameFromFile(f.name));
                }}
              />
            )}
          </Field>
        )}
        <Field label="Name" hint="For example “Intel I210 network, 12.19”.">
          {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        {problem && <Alert tone="error">{problem}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={busy || (!!file && !!archiveProblem(file.name))}>
            {existing ? (busy ? "Starting…" : "Import") : "Upload and import"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
