import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime } from "@/lib/format";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { describeKind, refreshImages, signerLabel, signerTone, type Image } from "./data";

export function ImageDetailPage() {
  const { imageId = "" } = useParams();
  const navigate = useNavigate();
  const image = useQuery({
    queryKey: ["images", imageId],
    queryFn: async () => unwrap(await api.GET("/images/{imageId}", { params: { path: { imageId } } })),
    // While importing, events refresh this; poll slowly in case the stream is down.
    refetchInterval: (q) => (q.state.data?.status === "importing" ? 5_000 : false),
  });
  const [confirmDelete, setConfirmDelete] = useState(false);

  const del = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/images/{imageId}", { params: { path: { imageId } } })),
    onSuccess: () => {
      refreshImages();
      void navigate("/images");
    },
  });

  const back = (
    <Link to="/images" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Images
    </Link>
  );

  if (image.isPending) return <p className="text-muted-foreground">Loading image…</p>;
  if (image.isError)
    return (
      <div className="max-w-4xl">
        {back}
        <Alert tone="error" title={image.error instanceof ApiError && image.error.status === 404 ? "No such image" : "The image couldn’t be loaded"}>
          {image.error instanceof ApiError && image.error.status === 404 ? "It may have been deleted." : errorMessage(image.error)}
        </Alert>
      </div>
    );

  const im = image.data;
  const win = im.details?.kind === "windows" ? im.details : null;
  const bootFiles = win ? win.bootManagers : im.details?.kind === "other" ? (im.details.bootFiles ?? []) : [];
  const has2023 = win?.bootManagers.some((b) => b.signer === "windows-uefi-ca-2023");

  return (
    <div className="max-w-4xl">
      {back}
      <PageHeader
        title={im.name}
        description={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {im.status === "ready" ? <Badge tone="success">Ready</Badge> : im.status === "failed" ? <Badge tone="error">Import failed</Badge> : <Badge>Importing</Badge>}
            <span>{describeKind(im)}</span>
          </span>
        }
        actions={
          <Button variant="destructive-ghost" onClick={() => { del.reset(); setConfirmDelete(true); }}>
            <Trash2 aria-hidden /> Delete
          </Button>
        }
      />

      <div className="space-y-6">
        {im.status === "failed" && (
          <Alert tone="error" title={im.error?.title || "The import failed"}>
            {im.error?.detail || "No further detail was recorded."}
          </Alert>
        )}
        {im.warnings?.map((w, i) => (
          <Alert key={i} tone="warning">
            {w}
          </Alert>
        ))}
        {win && !has2023 && (
          <Alert tone="warning" title="No boot manager signed with the Windows UEFI CA 2023">
            Machines that no longer trust the 2011 certificate won’t start a boot image built from this media. Use newer
            media for those.
          </Alert>
        )}

        <DetailsForm image={im} />

        <Section title="Source">
          <dl className="grid gap-x-6 gap-y-4 text-sm sm:grid-cols-2">
            <Item label="File name">{im.sourceFilename || "–"}</Item>
            <Item label="Volume label">{im.volumeLabel || "–"}</Item>
            <Item label="Size">{formatBytes(im.sizeBytes)}</Item>
            <Item label="Added">{formatDateTime(im.createdAt)}</Item>
            {win && <Item label="Install image format">{win.installImageFormat.toUpperCase()}</Item>}
            <Item label="Original ISO">{im.retainIso ? "Kept" : "Not kept"}</Item>
            <Item label="SHA-256" wide>
              <code className="font-mono text-xs break-all">{im.sha256 || "–"}</code>
            </Item>
          </dl>
        </Section>

        {win && (
          <Section title="Editions" description="The Windows editions a profile can install from this media.">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b">
                    <th scope="col" className={th}>Index</th>
                    <th scope="col" className={th}>Edition</th>
                    <th scope="col" className={th}>Build</th>
                    <th scope="col" className={th}>Languages</th>
                    <th scope="col" className={th}>Installed size</th>
                  </tr>
                </thead>
                <tbody>
                  {win.editions.map((e) => (
                    <tr key={e.index} className="border-b last:border-0">
                      <td className={`${td} tabular-nums`}>{e.index}</td>
                      <td className={td}>
                        {e.name}
                        {e.editionId && <span className="text-muted-foreground"> · {e.editionId}</span>}
                      </td>
                      <td className={`${td} tabular-nums`}>{e.build || "–"}</td>
                      <td className={td}>{e.languages?.join(", ") || e.defaultLanguage || "–"}</td>
                      <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(e.sizeBytes)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Section>
        )}

        {bootFiles.length > 0 && (
          <Section
            title={win ? "Boot managers" : "Boot files"}
            description="Every boot manager found on the media and inside its WIM files, with the certificate that signed it. Secure Boot firmware starts only those it trusts."
          >
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b">
                    <th scope="col" className={th}>Signed by</th>
                    <th scope="col" className={th}>Found at</th>
                    <th scope="col" className={th}>Architecture</th>
                  </tr>
                </thead>
                <tbody>
                  {bootFiles.map((b) => (
                    <tr key={b.sourcePath + b.sha256} className="border-b last:border-0">
                      <td className={`${td} whitespace-nowrap`}>
                        <Badge tone={signerTone[b.signer]}>{signerLabel[b.signer]}</Badge>
                      </td>
                      <td className={td}>
                        <code className="font-mono text-xs break-all" title={`SHA-256 ${b.sha256}`}>
                          {b.sourcePath}
                        </code>
                      </td>
                      <td className={td}>{b.architecture || "–"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {win && win.bootImageIndexes.length > 0 && (
              <p className="mt-4 text-sm text-muted-foreground">
                boot.wim contains: {win.bootImageIndexes.map((b) => `${b.index} ${b.name ?? ""}`.trim()).join("; ")}.
              </p>
            )}
          </Section>
        )}
      </div>

      <Dialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        title="Delete image?"
        footer={
          <>
            <Button onClick={() => setConfirmDelete(false)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending} onClick={() => del.mutate()}>
              {del.isPending ? "Deleting…" : "Delete image"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          <span className="font-medium">{im.name}</span> and its extracted files will be removed. Its upload, if still on
          the server, is kept.
        </p>
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {del.error instanceof ApiError && del.error.code === "image_in_use"
              ? "A boot image or profile still uses this image. Change or delete those first."
              : errorMessage(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}

function Item({ label, wide, children }: { label: string; wide?: boolean; children: ReactNode }) {
  return (
    <div className={wide ? "sm:col-span-2" : undefined}>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5">{children}</dd>
    </div>
  );
}

/** Name and notes: the only parts of an image that can be edited. */
function DetailsForm({ image: im }: { image: Image }) {
  const qc = useQueryClient();
  const [name, setName] = useState(im.name);
  const [notes, setNotes] = useState(im.notes ?? "");
  const dirty = name !== im.name || notes !== (im.notes ?? "");

  // Take the server's values when they change and there are no edits to lose.
  useEffect(() => {
    setName(im.name);
    setNotes(im.notes ?? "");
  }, [im.etag]);

  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/images/{imageId}", {
          params: { path: { imageId: im.id }, header: { "If-Match": im.etag } },
          body: { name: name.trim(), notes },
        }),
      ),
    onSuccess: (data) => {
      qc.setQueryData(["images", im.id], data);
      void qc.invalidateQueries({ queryKey: ["images"], exact: true });
    },
  });
  const stale = save.error instanceof ApiError && save.error.status === 412;

  return (
    <Section title="Details">
      <form
        className="space-y-5"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <Field label="Name">{(p) => <Input {...p} maxLength={200} required value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Notes (optional)">{(p) => <Textarea {...p} rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} />}</Field>
        {save.isError && (
          <Alert tone="error">
            {stale ? "This image was changed somewhere else. Reload the page, then make your change again." : errorMessage(save.error)}
          </Alert>
        )}
        <div className="flex items-center justify-end gap-3">
          {save.isSuccess && !dirty && <span className="text-sm text-muted-foreground" role="status">Saved.</span>}
          <Button type="submit" variant="primary" disabled={!dirty || !name.trim() || save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </form>
    </Section>
  );
}
