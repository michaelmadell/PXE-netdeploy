import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Hammer, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import { api } from "@/api/client";
import { JobProgress, useActiveJobs } from "@/components/JobProgress";
import { Button } from "@/components/ui/button";
import { Alert, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime } from "@/lib/format";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { signerLabel, useImages } from "@/pages/images/data";
import { BootImageForm } from "./BootImageForm";
import { BootImageStatus } from "./BootImages";
import { buildBootImage, buildErrorMessage, refreshBootImages, type BootImageInput } from "./data";

const artifactKind = { wim: "Windows PE", bcd: "Boot configuration", bootmgr: "Boot manager", sdi: "RAM disk header", font: "Font", "http-boot-iso": "HTTP Boot ISO" } as const;

export function BootImageDetailPage() {
  const { bootImageId = "" } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  // Set by the list page when "Create and build" made the image but the build was refused.
  const refused = (useLocation().state as { buildError?: string } | null)?.buildError;
  const images = useImages();
  const jobs = useActiveJobs("bootImage.build");
  const bootImage = useQuery({
    queryKey: ["boot-images", bootImageId],
    queryFn: async () => unwrap(await api.GET("/boot-images/{bootImageId}", { params: { path: { bootImageId } } })),
    refetchInterval: (q) => (q.state.data?.status === "building" ? 5_000 : false),
  });
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const build = useMutation({
    mutationFn: (force: boolean) => buildBootImage(bootImageId, force),
    onMutate: () => setNote(null),
    onSuccess: (r) => setNote(r === "current" ? "Already up to date: an identical build exists, so nothing was rebuilt." : null),
  });
  const save = useMutation({
    mutationFn: async (input: BootImageInput) =>
      unwrap(
        await api.PUT("/boot-images/{bootImageId}", {
          params: { path: { bootImageId }, header: { "If-Match": bootImage.data!.etag } },
          body: input,
        }),
      ),
    onSuccess: (data) => {
      qc.setQueryData(["boot-images", bootImageId], data);
      refreshBootImages();
    },
  });
  const del = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/boot-images/{bootImageId}", { params: { path: { bootImageId } } })),
    onSuccess: () => {
      refreshBootImages();
      void navigate("/boot-images");
    },
  });

  const back = (
    <Link to="/boot-images" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Boot images
    </Link>
  );
  if (bootImage.isPending) return <p className="text-muted-foreground">Loading boot image…</p>;
  if (bootImage.isError) {
    const gone = bootImage.error instanceof ApiError && bootImage.error.status === 404;
    return (
      <div className="max-w-4xl">
        {back}
        <Alert tone="error" title={gone ? "No such boot image" : "The boot image couldn’t be loaded"}>
          {gone ? "It may have been deleted." : errorMessage(bootImage.error)}
        </Alert>
      </div>
    );
  }

  const b = bootImage.data;
  const job = jobs.data?.items.find((j) => j.resourceId === b.id);
  const busy = !!job || b.status === "building";
  const built = (b.artifacts?.length ?? 0) > 0;

  return (
    <div className="max-w-4xl">
      {back}
      <PageHeader
        title={b.name}
        description={`${b.architecture} · from ${images.data?.items.find((im) => im.id === b.sourceImageId)?.name ?? "a deleted image"}`}
        actions={
          <>
            <Button variant={b.status === "ready" ? "secondary" : "primary"} disabled={busy || build.isPending} onClick={() => build.mutate(b.status === "ready")}>
              <Hammer aria-hidden /> {built ? "Rebuild" : "Build"}
            </Button>
            <Button variant="destructive-ghost" onClick={() => { del.reset(); setConfirmDelete(true); }}>
              <Trash2 aria-hidden /> Delete
            </Button>
          </>
        }
      />

      <div className="space-y-6">
        {(build.isError || (refused && !built && b.status === "draft" && !busy && !build.isSuccess)) && (
          <Alert tone="error" title="The build couldn’t be started">
            {build.isError ? buildErrorMessage(build.error) : refused}
          </Alert>
        )}
        {note && <Alert tone="info">{note}</Alert>}

        <Section title="Status">
          {job ? <JobProgress job={job} what="Build" queuedText="Waiting for another build to finish" /> : <BootImageStatus image={b} />}
          {b.warnings?.map((w, i) => (
            <Alert key={i} tone="warning" className="mt-3">
              {w}
            </Alert>
          ))}
          {built && (
            <dl className="mt-4 grid gap-x-6 gap-y-4 text-sm sm:grid-cols-3">
              <div>
                <dt className="text-muted-foreground">Built</dt>
                <dd className="mt-0.5">
                  {formatDateTime(b.builtAt)}
                  {b.builtWith && ` with ${b.builtWith === "dism" ? "DISM" : "wimlib"}`}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Boot manager signed by</dt>
                <dd className="mt-0.5">{b.selectedBootManager ? signerLabel[b.selectedBootManager.signer] : "–"}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Download size</dt>
                <dd className="mt-0.5 tabular-nums">{formatBytes(b.sizeBytes)}</dd>
              </div>
            </dl>
          )}
          {built && b.selectedBootManager?.signer === "windows-pca-2011" && (
            <Alert tone="warning" className="mt-4" title="Signed with the 2011 certificate">
              Machines that trust only the Windows UEFI CA 2023 will refuse to start this. Build from newer media for those.
            </Alert>
          )}
        </Section>

        <Section title="Settings" description="Changing these marks the boot image out of date. It keeps serving its last build until you rebuild.">
          <BootImageForm key={b.etag} initial={b} images={images.data?.items ?? []} error={save.error} onSubmit={(input) => save.mutate(input)}>
            {({ dirty, valid }) => (
              <div className="flex items-center justify-end gap-3">
                {save.isSuccess && !dirty && (
                  <span className="text-sm text-muted-foreground" role="status">
                    Saved. Rebuild to apply.
                  </span>
                )}
                <Button type="submit" variant="primary" disabled={!dirty || !valid || save.isPending}>
                  {save.isPending ? "Saving…" : "Save"}
                </Button>
              </div>
            )}
          </BootImageForm>
        </Section>

        {built && (
          <Section title="Files served" description="What a booting machine downloads, over TFTP or HTTP.">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b">
                    <th scope="col" className={th}>File</th>
                    <th scope="col" className={th}>What it is</th>
                    <th scope="col" className={th}>Size</th>
                  </tr>
                </thead>
                <tbody>
                  {b.artifacts!.map((a) => (
                    <tr key={a.path} className="border-b last:border-0">
                      <td className={td}>
                        <code className="font-mono text-xs break-all" title={`SHA-256 ${a.sha256}`}>
                          {a.path}
                        </code>
                      </td>
                      <td className={td}>{artifactKind[a.kind]}</td>
                      <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(a.sizeBytes)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Section>
        )}
      </div>

      <Dialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        title="Delete boot image?"
        footer={
          <>
            <Button onClick={() => setConfirmDelete(false)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending} onClick={() => del.mutate()}>
              {del.isPending ? "Deleting…" : "Delete boot image"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          <span className="font-medium">{b.name}</span> and its built files will be removed. The Windows image it was built
          from is kept.
        </p>
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {del.error instanceof ApiError && del.error.code === "boot_image_in_use"
              ? "A profile still uses this boot image. Change or delete that profile first."
              : del.error instanceof ApiError && del.error.code === "build_in_progress"
                ? "A build is queued or running. Cancel it first."
                : errorMessage(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}
