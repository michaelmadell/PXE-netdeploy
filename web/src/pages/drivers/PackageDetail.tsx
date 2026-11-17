import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Pencil, RotateCcw, Save, Trash2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input, Select } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime } from "@/lib/format";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { useBootImages, useDriverGroups } from "@/pages/boot/data";
import { useProfiles } from "@/pages/clients/data";
import {
  contents,
  driverError,
  groupsWith,
  groupUsers,
  osVersionText,
  refreshDrivers,
  roleText,
  signatureHelp,
  signatureText,
  signatureTone,
  useDriverPackage,
  type DriverInf,
  type Role,
  type Signature,
} from "./data";

type Filter = { role: Role | ""; arch: string; sig: Signature | ""; text: string };

export function DriverPackageDetailPage() {
  const { driverPackageId = "" } = useParams();
  const pkg = useDriverPackage(driverPackageId);
  const groups = useDriverGroups();
  const bootImages = useBootImages();
  const profiles = useProfiles();
  const navigate = useNavigate();
  const qc = useQueryClient();
  // INF id → enabled, for the INFs changed and not yet saved.
  const [changes, setChanges] = useState<Record<string, boolean>>({});
  const [filter, setFilter] = useState<Filter>({ role: "", arch: "", sig: "", text: "" });
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const p = pkg.data;
  const infs = p?.infs ?? [];
  const enabled = (i: DriverInf) => changes[i.id] ?? i.enabled;
  const changed = infs.filter((i) => changes[i.id] !== undefined && changes[i.id] !== i.enabled);
  const shown = useMemo(() => {
    const t = filter.text.trim().toLowerCase();
    return infs.filter(
      (i) =>
        (!filter.role || i.role === filter.role) &&
        (!filter.arch || i.architectures.includes(filter.arch as DriverInf["architectures"][number])) &&
        (!filter.sig || i.signature === filter.sig) &&
        (!t ||
          i.path.toLowerCase().includes(t) ||
          (i.provider ?? "").toLowerCase().includes(t) ||
          (i.hardwareIds ?? []).some((h) => h.toLowerCase().includes(t))),
    );
  }, [infs, filter]);

  const inGroups = p ? groupsWith(groups.data?.items, p.id!) : [];
  const affectedBoot = [
    ...new Map(inGroups.flatMap((g) => groupUsers(g.id!, bootImages.data?.items, undefined).bootImages).map((b) => [b.id, b])).values(),
  ];
  const affectedProfiles = [
    ...new Map(inGroups.flatMap((g) => groupUsers(g.id!, undefined, profiles.data?.items).profiles).map((x) => [x.id, x])).values(),
  ];
  const unsignedOn = infs.filter((i) => enabled(i) && i.signature === "unsigned");

  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/driver-packages/{driverPackageId}", {
          params: { path: { driverPackageId }, header: { "If-Match": p!.etag ?? "*" } },
          body: { infs: changed.map((i) => ({ id: i.id, enabled: changes[i.id]! })) },
        }),
      ),
    onSuccess: (saved) => {
      qc.setQueryData(["driver-package", driverPackageId], saved);
      setChanges({});
      refreshDrivers();
    },
  });
  const del = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/driver-packages/{driverPackageId}", { params: { path: { driverPackageId } } })),
    onSuccess: () => {
      refreshDrivers();
      navigate("/drivers");
    },
  });

  const back = (
    <Link to="/drivers" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Driver packages
    </Link>
  );
  if (pkg.isPending) return <p className="text-muted-foreground">Loading driver package…</p>;
  if (pkg.isError || !p) {
    const gone = pkg.error instanceof ApiError && pkg.error.status === 404;
    return (
      <div className="max-w-5xl">
        {back}
        <Alert tone="error" title={gone ? "No such driver package" : "The driver package couldn’t be loaded"}>
          {gone ? "It may have been deleted." : errorMessage(pkg.error)}
        </Alert>
      </div>
    );
  }

  const setAll = (on: boolean) =>
    setChanges((c) => {
      const next = { ...c };
      for (const i of shown) next[i.id] = on;
      return next;
    });
  const facts: [string, ReactNode][] = [
    p.status === "ready" ? ["Contains", contents(p)] : ["Status", p.status === "failed" ? "Import failed" : "Importing"],
    ["From", p.sourceFilename ?? "–"],
    ["Archive", `${formatBytes(p.sizeBytes)} · SHA-256 ${p.sha256 ?? "–"}`],
    ["Imported", p.createdAt ? formatDateTime(p.createdAt) : "–"],
    [
      "In groups",
      inGroups.length ? (
        inGroups.map((g, n) => (
          <span key={g.id}>
            {n > 0 && ", "}
            <Link to="/driver-groups" className="text-primary underline-offset-4 hover:underline">
              {g.name}
            </Link>
          </span>
        ))
      ) : (
        "None. Add it to a driver group to use it."
      ),
    ],
  ];

  return (
    <div className="max-w-6xl">
      {back}
      <PageHeader
        title={p.name}
        actions={
          <>
            <Button onClick={() => setRenaming(true)}>
              <Pencil aria-hidden /> Rename
            </Button>
            <Button variant="destructive-ghost" onClick={() => { del.reset(); setDeleting(true); }}>
              <Trash2 aria-hidden /> Delete
            </Button>
          </>
        }
      />

      {p.status === "failed" && (
        <Alert tone="error" className="mb-6" title="The import failed">
          {driverError(new ApiError(422, p.error ?? null))}
        </Alert>
      )}
      {(p.warnings?.length ?? 0) > 0 && (
        <Alert tone="warning" className="mb-6" title="Warnings from the import">
          <ul className="list-disc space-y-1 pl-5">
            {p.warnings!.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </Alert>
      )}
      {unsignedOn.length > 0 && affectedBoot.length > 0 && (
        <Alert tone="error" className="mb-6" title="Boot images using this package won’t build">
          {unsignedOn.length === 1 ? "An unsigned driver is" : `${unsignedOn.length} unsigned drivers are`} enabled, and WinPE under Secure Boot
          won’t load {unsignedOn.length === 1 ? "it" : "them"}. Disable {unsignedOn.length === 1 ? "it" : "them"}, or remove the package from{" "}
          {affectedBoot.map((b) => b.name).join(", ")}.
        </Alert>
      )}

      <Section title="Package" className="mb-6">
        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[10rem_1fr]">
          {facts.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="min-w-0 break-all">{v}</dd>
            </div>
          ))}
        </dl>
      </Section>

      <Section
        title={`Drivers (${infs.length})`}
        description="Each INF in the package. Disabled ones are left out wherever the package is used."
        actions={
          <div className="flex gap-2">
            <Button size="sm" disabled={shown.length === 0} onClick={() => setAll(true)}>
              Enable all shown
            </Button>
            <Button size="sm" disabled={shown.length === 0} onClick={() => setAll(false)}>
              Disable all shown
            </Button>
          </div>
        }
      >
        <div className="mb-4 grid gap-3 sm:grid-cols-4">
          <Field label="Type">
            {(f) => (
              <Select {...f} value={filter.role} onChange={(e) => setFilter({ ...filter, role: e.target.value as Role | "" })}>
                <option value="">All</option>
                <option value="network">Network</option>
                <option value="storage">Storage</option>
                <option value="other">Other</option>
              </Select>
            )}
          </Field>
          <Field label="Architecture">
            {(f) => (
              <Select {...f} value={filter.arch} onChange={(e) => setFilter({ ...filter, arch: e.target.value })}>
                <option value="">All</option>
                <option value="x64">x64</option>
                <option value="arm64">arm64</option>
              </Select>
            )}
          </Field>
          <Field label="Signature">
            {(f) => (
              <Select {...f} value={filter.sig} onChange={(e) => setFilter({ ...filter, sig: e.target.value as Signature | "" })}>
                <option value="">All</option>
                {(Object.keys(signatureText) as Signature[]).map((s) => (
                  <option key={s} value={s}>
                    {signatureText[s]}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Search">
            {(f) => (
              <Input {...f} placeholder="Path, provider, hardware ID" value={filter.text} onChange={(e) => setFilter({ ...filter, text: e.target.value })} />
            )}
          </Field>
        </div>

        {shown.length === 0 ? (
          <p className="text-sm text-muted-foreground">No drivers match.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b">
                  <th scope="col" className={th}>Use</th>
                  <th scope="col" className={th}>INF</th>
                  <th scope="col" className={th}>Type</th>
                  <th scope="col" className={th}>Version</th>
                  <th scope="col" className={th}>Runs on</th>
                  <th scope="col" className={th}>Signature</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((i) => (
                  <InfRow
                    key={i.id}
                    inf={i}
                    enabled={enabled(i)}
                    changed={changes[i.id] !== undefined && changes[i.id] !== i.enabled}
                    onToggle={(on) => setChanges((c) => ({ ...c, [i.id]: on }))}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      {(changed.length > 0 || save.isError) && (
        <div className="sticky bottom-0 mt-6 -mx-2 rounded-lg border bg-popover px-5 py-4 shadow-lg">
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="max-w-prose text-sm">
              <p className="font-medium">
                {changed.length} unsaved change{changed.length === 1 ? "" : "s"}
              </p>
              <p className="mt-0.5 text-muted-foreground">
                {affectedBoot.length > 0
                  ? `${affectedBoot.map((b) => b.name).join(", ")} will be out of date and need building again.`
                  : "No boot image uses this package yet."}
                {affectedProfiles.length > 0 && ` Profiles ${affectedProfiles.map((x) => x.name).join(", ")} install the new selection from their next deployment.`}
              </p>
              {save.isError && (
                <p role="alert" className="mt-1 text-destructive">
                  {save.error instanceof ApiError && save.error.status === 412
                    ? "Someone changed this package somewhere else. Discard, then make your changes again."
                    : driverError(save.error)}
                </p>
              )}
            </div>
            <div className="flex gap-2">
              <Button onClick={() => { setChanges({}); save.reset(); }}>
                <RotateCcw aria-hidden /> Discard
              </Button>
              <Button variant="primary" disabled={save.isPending || changed.length === 0} onClick={() => save.mutate()}>
                <Save aria-hidden /> {save.isPending ? "Saving…" : "Save"}
              </Button>
            </div>
          </div>
        </div>
      )}

      <RenameDialog open={renaming} onClose={() => setRenaming(false)} id={driverPackageId} name={p.name} etag={p.etag} />
      <Dialog
        open={deleting}
        onClose={() => setDeleting(false)}
        title="Delete driver package?"
        footer={
          <>
            <Button onClick={() => setDeleting(false)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending || inGroups.length > 0} onClick={() => del.mutate()}>
              {del.isPending ? "Deleting…" : "Delete package"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          {inGroups.length > 0 ? (
            <>
              <span className="font-medium">{p.name}</span> is in {inGroups.map((g) => g.name).join(", ")}. Remove it from{" "}
              {inGroups.length === 1 ? "that group" : "those groups"} first.
            </>
          ) : (
            <>
              <span className="font-medium">{p.name}</span> and its files will be removed. Boot images already built keep the drivers they were
              built with until they are built again.
            </>
          )}
        </p>
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {driverError(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}

function InfRow({ inf: i, enabled, changed, onToggle }: { inf: DriverInf; enabled: boolean; changed: boolean; onToggle: (on: boolean) => void }) {
  const ids = i.hardwareIds ?? [];
  const noArch = i.architectures.length === 0;
  return (
    <tr className={`border-b last:border-0 ${changed ? "bg-accent/40" : ""}`}>
      <td className={td}>
        <Checkbox
          label={<span className="sr-only">Use {i.path}</span>}
          checked={enabled}
          disabled={noArch && !enabled}
          onChange={onToggle}
        />
      </td>
      <td className={`${td} max-w-md`}>
        <span className="font-mono text-xs break-all">{i.path}</span>
        {i.provider && <div className="text-xs text-muted-foreground">{i.provider}</div>}
        {ids.length > 0 && (
          <details className="mt-1 text-xs">
            <summary className="cursor-pointer text-muted-foreground">
              {ids.length} hardware ID{ids.length === 1 ? "" : "s"}
            </summary>
            <ul className="mt-1 max-h-48 space-y-0.5 overflow-y-auto font-mono break-all">
              {ids.map((h) => (
                <li key={h}>{h}</li>
              ))}
            </ul>
          </details>
        )}
      </td>
      <td className={td}>
        {roleText[i.role]}
        {i.role === "storage" && <div className="text-xs text-muted-foreground">Needed at boot</div>}
        <div className="text-xs text-muted-foreground">{i.class}</div>
      </td>
      <td className={`${td} whitespace-nowrap`}>
        {i.version ?? "–"}
        {i.driverDate && <div className="text-xs text-muted-foreground">{i.driverDate}</div>}
      </td>
      <td className={td}>
        {noArch ? <span className="text-muted-foreground">Not x64 or arm64</span> : i.architectures.join(", ")}
        {(i.targetOsVersions ?? []).map((v) => (
          <div key={v} className="text-xs text-muted-foreground">
            {osVersionText(v)}
          </div>
        ))}
      </td>
      <td className={`${td} max-w-xs`}>
        <span title={signatureHelp[i.signature]}>
          <Badge tone={signatureTone[i.signature]}>{signatureText[i.signature]}</Badge>
        </span>
        {i.signatureDetail && <div className="mt-1 text-xs text-muted-foreground">{i.signatureDetail}</div>}
        {i.signer && <div className="mt-1 text-xs text-muted-foreground break-words">{i.signer}</div>}
      </td>
    </tr>
  );
}

function RenameDialog({ open, onClose, id, name, etag }: { open: boolean; onClose: () => void; id: string; name: string; etag?: string }) {
  const qc = useQueryClient();
  const [value, setValue] = useState(name);
  const rename = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/driver-packages/{driverPackageId}", {
          params: { path: { driverPackageId: id }, header: { "If-Match": etag ?? "*" } },
          body: { name: value.trim() },
        }),
      ),
    onSuccess: (saved) => {
      qc.setQueryData(["driver-package", id], saved);
      refreshDrivers();
      onClose();
    },
  });
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Rename driver package"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!value.trim() || rename.isPending} onClick={() => rename.mutate()}>
            {rename.isPending ? "Saving…" : "Rename"}
          </Button>
        </>
      }
    >
      <Field label="Name">{(f) => <Input {...f} maxLength={200} value={value} onChange={(e) => setValue(e.target.value)} />}</Field>
      {rename.isError && (
        <Alert tone="error" className="mt-4">
          {rename.error instanceof ApiError && rename.error.status === 412 ? "It was changed somewhere else. Close and try again." : driverError(rename.error)}
        </Alert>
      )}
    </Dialog>
  );
}
