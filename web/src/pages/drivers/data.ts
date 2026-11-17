import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { useActiveJobs } from "@/components/JobProgress";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { failUpload, removeUpload, startUpload } from "@/lib/tus";
import { newKey } from "@/pages/images/data";

export type DriverPackage = Schemas["DriverPackage"];
export type DriverInf = Schemas["DriverInf"];
export type DriverGroup = Schemas["DriverGroup"];
export type Role = Schemas["DriverRole"];
export type Signature = Schemas["DriverSignature"];

/** Query keys: ["driver-packages", filter], ["driver-package", id]; groups use ["driver-groups"] (boot/data.ts). */
export function useDriverPackages(hardwareId = "") {
  return useQuery({
    queryKey: ["driver-packages", { hardwareId }],
    queryFn: async () => {
      const all: DriverPackage[] = [];
      let cursor: string | undefined;
      do {
        const page = unwrap(
          await api.GET("/driver-packages", { params: { query: { limit: 200, cursor, ...(hardwareId ? { hardwareId } : {}) } } }),
        );
        all.push(...page.items);
        cursor = page.nextCursor ?? undefined;
      } while (cursor);
      return all;
    },
  });
}

export function useDriverPackage(id: string) {
  return useQuery({
    queryKey: ["driver-package", id],
    queryFn: async () => unwrap(await api.GET("/driver-packages/{driverPackageId}", { params: { path: { driverPackageId: id } } })),
  });
}

export const useDriverImportJobs = () => useActiveJobs("driverPackage.import");

export function refreshDrivers() {
  for (const key of ["driver-packages", "driver-package", "driver-groups", "boot-images", "jobs", "uploads", "health"])
    void queryClient.invalidateQueries({ queryKey: [key] });
}

/** Why a file can't be a driver package, before uploading it; null if it can. */
export function archiveProblem(filename: string): string | null {
  if (/\.(exe|msi)$/i.test(filename))
    return "Installers can’t be imported. Extract the drivers first (most vendors document an extract-only switch, such as /extract or /a) and upload them as a zip.";
  if (!/\.(zip|cab|7z)$/i.test(filename)) return "Choose a .zip, .cab or .7z file containing the driver folders (.inf, .sys, .cat).";
  return null;
}

/** "Intel-I210-v12.zip" → "Intel-I210-v12". */
export const packageNameFromFile = (f: string) => f.replace(/\.(zip|cab|7z)$/i, "");

/** Queues the import of a completed upload. The key makes a retry return the same job. */
export async function importDriverUpload(uploadId: string, name: string, idempotencyKey: string) {
  const job = unwrap(
    await api.POST("/driver-packages", { params: { header: { "Idempotency-Key": idempotencyKey } }, body: { uploadId, name } }),
  );
  refreshDrivers();
  return job;
}

/** Uploads an archive, then imports it. Carries on if the user leaves the page. */
export function uploadAndImportDriver(file: File, name: string) {
  let idempotencyKey = newKey();
  startUpload(
    file,
    (uploadId, key) => {
      importDriverUpload(uploadId, name, idempotencyKey).then(
        () => void removeUpload(key, { keep: true }),
        (e: unknown) => {
          idempotencyKey = newKey();
          failUpload(key, `Uploaded, but the import couldn’t start. ${driverError(e)}`);
        },
      );
    },
    "driver",
  );
}

export const roleText: Record<Role, string> = { network: "Network", storage: "Storage", other: "Other" };

export const signatureText: Record<Signature, string> = {
  whql: "Microsoft-signed",
  vendor: "Vendor-signed",
  unsigned: "Unsigned",
  unknown: "Signature unknown",
};
export const signatureTone: Record<Signature, "success" | "warning" | "error"> = {
  whql: "success",
  vendor: "warning",
  unsigned: "error",
  unknown: "warning",
};
export const signatureHelp: Record<Signature, string> = {
  whql: "Signed through Microsoft (WHQL, attestation or inbox). Loads under Secure Boot.",
  vendor: "Signed by the vendor, not Microsoft. Windows with Secure Boot won’t load it as a kernel driver.",
  unsigned: "No valid signature, or a file it installs isn’t covered. Boot images refuse to build with it enabled.",
  unknown: "Has a catalog this server can’t check.",
};

/** "2 network, 1 storage" for the enabled INFs; disabled ones are counted apart. */
export function contents(p: DriverPackage): string {
  const on = p.infs.filter((i) => i.enabled);
  const parts = (["network", "storage", "other"] as Role[])
    .map((r) => [r, on.filter((i) => i.role === r).length] as const)
    .filter(([, n]) => n > 0)
    .map(([r, n]) => `${n} ${roleText[r].toLowerCase()}`);
  const off = p.infs.length - on.length;
  return [parts.join(", ") || "No drivers enabled", off ? `${off} disabled` : ""].filter(Boolean).join(" · ");
}

const worst: Signature[] = ["unsigned", "unknown", "vendor", "whql"];
/** The signing state that matters most among the enabled INFs, with how many have it. */
export function signingSummary(p: DriverPackage): { sig: Signature; count: number; text: string } | null {
  const on = p.infs.filter((i) => i.enabled);
  if (on.length === 0) return null;
  for (const sig of worst) {
    const n = on.filter((i) => i.signature === sig).length;
    if (n === 0) continue;
    if (sig === "whql") return { sig, count: n, text: n === on.length ? "All Microsoft-signed" : `${n} Microsoft-signed` };
    return { sig, count: n, text: `${n} ${signatureText[sig].toLowerCase()}` };
  }
  return null;
}

/** "10.0...22000" → "Windows 11 (build 22000) and later". */
export function osVersionText(v: string): string {
  const m = /^10\.0\.\.\.(\d+)$/.exec(v) ?? /^10\.0\.\.\.(\d+)\b/.exec(v);
  if (!m) return v;
  const build = Number(m[1]);
  return `${build >= 22000 ? "Windows 11" : "Windows 10"} (build ${build}) and later`;
}

/** Groups that include a package. */
export const groupsWith = (groups: DriverGroup[] | undefined, packageId: string) =>
  (groups ?? []).filter((g) => g.driverPackageIds.includes(packageId));

/** Boot images and profiles that use a group. */
export function groupUsers(
  groupId: string,
  bootImages: Schemas["BootImage"][] | undefined,
  profiles: Schemas["Profile"][] | undefined,
): { bootImages: Schemas["BootImage"][]; profiles: Schemas["Profile"][] } {
  return {
    bootImages: (bootImages ?? []).filter((b) => b.driverGroupIds?.includes(groupId)),
    profiles: (profiles ?? []).filter((p) => p.osDriverGroupIds?.includes(groupId)),
  };
}

const codes: Record<string, string> = {
  package_in_use: "Driver groups still include this package. Remove it from them first.",
  driver_group_in_use: "A boot image or a profile still uses this group. Remove it from them first.",
  unknown_driver_package: "One of those packages no longer exists, or failed to import.",
  unsupported_archive: "That file can’t be imported as a driver package.",
  invalid_archive: "The archive is damaged, or expands to far more than its size.",
  no_drivers: "There are no .inf files in the archive.",
  insufficient_storage: "There isn’t enough free space on the server to import it.",
  upload_incomplete: "The upload hasn’t finished.",
  upload_in_use: "Another import is using that upload.",
};
export function driverError(e: unknown): string {
  if (e instanceof ApiError && codes[e.code]) return e.message && !e.message.startsWith(codes[e.code]!) ? `${codes[e.code]} ${e.message}` : codes[e.code]!;
  return errorMessage(e);
}
