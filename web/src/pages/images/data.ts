/** Queries and commands shared by the images list and detail pages. */
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { useActiveJobs } from "@/components/JobProgress";
import { errorMessage, unwrap } from "@/lib/problem";
import { failUpload, removeUpload, startUpload } from "@/lib/tus";

export type Image = Schemas["Image"];
export type Job = Schemas["Job"];
export type Upload = Schemas["Upload"];
export type Signer = Schemas["SecureBootSigner"];

export function useImages() {
  return useQuery({
    queryKey: ["images"],
    queryFn: async () => unwrap(await api.GET("/images", { params: { query: { limit: 200 } } })),
  });
}

/** Queued and running imports, to show progress beside the image they belong to. */
export const useImportJobs = () => useActiveJobs("image.import");

export function useUploadList() {
  return useQuery({
    queryKey: ["uploads"],
    queryFn: async () => unwrap(await api.GET("/uploads", { params: { query: { limit: 200 } } })),
  });
}

export function refreshImages() {
  for (const key of ["images", "jobs", "uploads", "health"]) void queryClient.invalidateQueries({ queryKey: [key] });
}

export type ImportOptions = { name: string; retainIso: boolean };

/** Queues the import of a completed upload. The key makes a retry return the same job. */
export async function importUpload(uploadId: string, opts: ImportOptions, idempotencyKey: string) {
  const job = unwrap(
    await api.POST("/images", {
      params: { header: { "Idempotency-Key": idempotencyKey } },
      body: { uploadId, name: opts.name, retainIso: opts.retainIso },
    }),
  );
  refreshImages();
  return job;
}

/** A random Idempotency-Key. Not randomUUID: that needs HTTPS, and `just dev` over the LAN is plain HTTP. */
export function newKey(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Uploads a file, then imports it. Carries on if the user leaves the page. */
export function uploadAndImport(file: File, opts: ImportOptions) {
  let idempotencyKey = newKey();
  startUpload(file, (uploadId, key) => {
    importUpload(uploadId, opts, idempotencyKey).then(
      () => void removeUpload(key, { keep: true }),
      (e: unknown) => {
        // A refused request is replayed for its key, so "Try again" needs a new one.
        idempotencyKey = newKey();
        failUpload(key, `Uploaded, but the import couldn’t start. ${errorMessage(e)}`);
      },
    );
  });
}

/** "Windows-11.iso" → "Windows-11": a starting point for the image's name. */
export function nameFromFile(filename: string): string {
  return filename.replace(/\.(iso|img)$/i, "").slice(0, 200);
}

export const signerLabel: Record<Signer, string> = {
  "windows-uefi-ca-2023": "Windows UEFI CA 2023",
  "windows-pca-2011": "Windows Production PCA 2011",
  "uefi-ca-2023": "Microsoft UEFI CA 2023 (third-party)",
  "uefi-ca-2011": "Microsoft UEFI CA 2011 (third-party)",
  unsigned: "Unsigned",
  unknown: "Unknown signer",
};

export const signerTone: Record<Signer, "success" | "neutral" | "warning" | "error"> = {
  "windows-uefi-ca-2023": "success",
  "windows-pca-2011": "neutral",
  "uefi-ca-2023": "success",
  "uefi-ca-2011": "neutral",
  unsigned: "error",
  unknown: "warning",
};

export function describeKind(im: Image): string {
  if (im.details?.kind === "windows") return `Windows · ${im.details.architecture}`;
  if (im.kind === "windows") return "Windows";
  if (im.kind === "linux") return "Linux";
  if (im.kind === "other") return "Other";
  return "–";
}
