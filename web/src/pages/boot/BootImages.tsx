import { useMutation } from "@tanstack/react-query";
import { Hammer, Plus } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "@/api/client";
import { JobProgress, useActiveJobs } from "@/components/JobProgress";
import { Button } from "@/components/ui/button";
import { Alert, Badge, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorMessage, unwrap } from "@/lib/problem";
import { signerLabel, useImages } from "@/pages/images/data";
import { BootImageForm } from "./BootImageForm";
import { buildBootImage, buildErrorMessage, refreshBootImages, staleReasonText, useBootImages, type BootImage, type BootImageInput } from "./data";

/** What a boot image's status means for someone about to boot a machine. */
export function BootImageStatus({ image: b }: { image: BootImage }) {
  switch (b.status) {
    case "ready":
      return <Badge tone={b.warnings?.length ? "warning" : "success"}>{b.warnings?.length ? "Ready, with warnings" : "Ready"}</Badge>;
    case "draft":
      return <Badge>Not built</Badge>;
    case "building":
      return <Badge>Building</Badge>;
    case "stale":
      return (
        <>
          <Badge tone="warning">Out of date</Badge>
          <p className="mt-1 text-xs text-muted-foreground">
            Still serving the last build. Rebuild because {b.staleReasons?.map((r) => staleReasonText[r]).join(", ") || "its inputs changed"}.
          </p>
        </>
      );
    case "failed":
      return (
        <>
          <Badge tone="error">Build failed</Badge>
          {b.error?.detail && <p className="mt-1 text-xs text-muted-foreground">{b.error.detail}</p>}
          {(b.artifacts?.length ?? 0) > 0 && <p className="mt-1 text-xs text-muted-foreground">Still serving the last good build.</p>}
        </>
      );
  }
}

export function BootImagesPage() {
  const navigate = useNavigate();
  const bootImages = useBootImages();
  const images = useImages();
  const jobs = useActiveJobs("bootImage.build");
  const [creating, setCreating] = useState(false);

  // Create, then build straight away: a boot image that isn't built boots nothing.
  const create = useMutation({
    mutationFn: async (input: BootImageInput) => {
      const made = unwrap(await api.POST("/boot-images", { body: input }));
      refreshBootImages();
      try {
        await buildBootImage(made.id);
        return { made, buildError: null };
      } catch (e) {
        return { made, buildError: buildErrorMessage(e) };
      }
    },
    onSuccess: ({ made, buildError }) => {
      setCreating(false);
      // A refused build is explained on the boot image's own page.
      if (buildError) void navigate(`/boot-images/${made.id}`, { state: { buildError } });
    },
  });
  const build = useMutation({ mutationFn: (id: string) => buildBootImage(id) });

  const items = bootImages.data?.items ?? [];
  const imageName = (id: string) => images.data?.items.find((im) => im.id === id)?.name ?? "Deleted image";
  const jobFor = (b: BootImage) => jobs.data?.items.find((j) => j.resourceId === b.id);
  const newButton = (
    <Button variant="primary" onClick={() => { create.reset(); setCreating(true); }}>
      <Plus aria-hidden /> New boot image
    </Button>
  );

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Boot images"
        description="What a machine downloads and starts when it network boots: the Windows boot manager, its configuration, and Windows PE with the deployment agent added. Built from a Windows image."
        actions={newButton}
      />

      {build.isError && (
        <Alert tone="error" className="mb-6" title="The build couldn’t be started">
          {buildErrorMessage(build.error)}
        </Alert>
      )}

      {bootImages.isPending ? (
        <p className="text-muted-foreground">Loading boot images…</p>
      ) : bootImages.isError ? (
        <Alert tone="error" title="Boot images couldn’t be loaded">
          {errorMessage(bootImages.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No boot images yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Make one from a Windows image you have imported. Building takes a minute or two; after that, add it to a profile
            so machines can boot it.
          </p>
          <div className="mt-4 flex justify-center">{newButton}</div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Built from</th>
                <th scope="col" className={th}>Boot manager</th>
                <th scope="col" className={th}>Size</th>
                <th scope="col" className={th}>Built</th>
                <th scope="col" className={th}>Status</th>
                <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.map((b) => {
                const job = jobFor(b);
                return (
                  <tr key={b.id} className="border-b last:border-0">
                    <td className={td}>
                      <Link to={`/boot-images/${b.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                        {b.name}
                      </Link>
                      <div className="text-xs text-muted-foreground">{b.architecture}</div>
                    </td>
                    <td className={td}>{imageName(b.sourceImageId)}</td>
                    <td className={td}>{b.selectedBootManager ? signerLabel[b.selectedBootManager.signer] : "–"}</td>
                    <td className={`${td} tabular-nums whitespace-nowrap`}>{formatBytes(b.sizeBytes)}</td>
                    <td className={`${td} whitespace-nowrap`}>{formatDateTime(b.builtAt)}</td>
                    <td className={`${td} min-w-44`}>
                      {job ? <JobProgress job={job} what="Build" queuedText="Waiting for another build to finish" compact /> : <BootImageStatus image={b} />}
                    </td>
                    <td className={`${td} pr-0 text-right`}>
                      {!job && b.status !== "ready" && b.status !== "building" && (
                        <Button size="sm" disabled={build.isPending} aria-label={`Build ${b.name}`} onClick={() => build.mutate(b.id)}>
                          <Hammer aria-hidden /> {b.status === "draft" ? "Build" : "Rebuild"}
                        </Button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {items.some((b) => (b.artifacts?.length ?? 0) > 0) && (
        <p className="mt-6 text-sm text-muted-foreground">
          A built boot image is offered to machines through a{" "}
          <Link to="/profiles" className="text-primary underline underline-offset-4">
            profile
          </Link>
          , which pairs it with the Windows edition to install.
        </p>
      )}

      <Dialog open={creating} onClose={() => setCreating(false)} title="New boot image">
        {creating && (
          <BootImageForm images={images.data?.items ?? []} error={create.error} onSubmit={(input) => create.mutate(input)}>
            {({ valid }) => (
              <div className="flex justify-end gap-2">
                <Button onClick={() => setCreating(false)}>Cancel</Button>
                <Button type="submit" variant="primary" disabled={!valid || create.isPending}>
                  {create.isPending ? "Creating…" : "Create and build"}
                </Button>
              </div>
            )}
          </BootImageForm>
        )}
      </Dialog>
    </div>
  );
}
