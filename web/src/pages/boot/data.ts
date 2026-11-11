/** Queries and commands shared by the boot image pages (and by profiles, which pick a boot image). */
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { ApiError, unwrap } from "@/lib/problem";

export type BootImage = Schemas["BootImage"];
export type BootImageInput = Schemas["BootImageInput"];
export type Image = Schemas["Image"];
export type Settings = Schemas["Settings"];
export type HostInterface = Schemas["HostInterface"];

export function useBootImages() {
  return useQuery({
    queryKey: ["boot-images"],
    queryFn: async () => unwrap(await api.GET("/boot-images", { params: { query: { limit: 200 } } })),
  });
}

export function useCapabilities() {
  return useQuery({
    queryKey: ["capabilities"],
    queryFn: async () => unwrap(await api.GET("/system/capabilities")),
  });
}

export function useDriverGroups() {
  return useQuery({
    queryKey: ["driver-groups"],
    queryFn: async () => unwrap(await api.GET("/driver-groups", { params: { query: { limit: 200 } } })),
  });
}

export function useSettings() {
  return useQuery({ queryKey: ["settings"], queryFn: async () => unwrap(await api.GET("/settings")) });
}

/** Non-loopback adapters; an empty list if the server can't say (older server). */
export function useAdapters() {
  return useQuery({
    queryKey: ["host-interfaces"],
    queryFn: async () => {
      try {
        return unwrap(await api.GET("/system/interfaces")).items.filter((h) => !h.loopback);
      } catch {
        return [];
      }
    },
  });
}

export function refreshBootImages() {
  for (const key of ["boot-images", "jobs", "health"]) void queryClient.invalidateQueries({ queryKey: [key] });
}

/**
 * Where WinPE finds this server: one URL per deployment interface that has
 * HTTP on, at the address clients use. These are baked into a boot image.
 */
export function suggestedServerUrls(settings: Settings | undefined, adapters: HostInterface[] | undefined): string[] {
  const urls: string[] = [];
  for (const i of settings?.interfaces ?? []) {
    if (!i.services.http) continue;
    const host = i.advertisedAddress || adapters?.find((a) => a.name === i.name)?.addresses[0]?.address;
    if (!host) continue;
    urls.push(`http://${host}${i.services.httpPort === 80 ? "" : `:${i.services.httpPort}`}`);
  }
  return urls;
}

/** Ready Windows images: what a boot image (and a profile) can be made from. */
export function windowsImages(items: Image[] | undefined) {
  return (items ?? []).flatMap((im) => (im.status === "ready" && im.details?.kind === "windows" ? [{ image: im, win: im.details }] : []));
}

export const buildCodes: Record<string, string> = {
  builder_unavailable:
    "This server has nothing to build with. Install wimlib (wimlib-imagex) or, on Windows, run netdeployd elevated so it can use DISM.",
  unsigned_driver: "A driver in the selected groups is unsigned. Secure Boot machines won’t load it; disable it or remove its group.",
  driver_architecture: "A driver in the selected groups doesn’t support this architecture.",
  source_image_unavailable: "The source image is missing or not ready.",
  agent_unavailable: "This server has no agent for the image’s architecture. Run “just agent”, then restart the server.",
  build_in_progress: "A build of this boot image is already queued or running.",
  insufficient_storage: "Not enough free space: about four times the size of boot.wim is needed above the storage reserve.",
};

/** A build refusal in plain words, with the server's detail where it adds something. */
export function buildErrorMessage(e: unknown): string {
  if (e instanceof ApiError && buildCodes[e.code]) {
    const detail = e.message && !e.message.startsWith("The server answered") ? ` (${e.message})` : "";
    return buildCodes[e.code] + detail;
  }
  if (e instanceof ApiError) return e.message;
  return e instanceof TypeError ? "Can’t reach the server." : "The build couldn’t be started.";
}

/** Starts a build. Resolves to "queued", or "current" when an identical build already exists. */
export async function buildBootImage(id: string, force = false): Promise<"queued" | "current"> {
  const r = await api.POST("/boot-images/{bootImageId}/build", { params: { path: { bootImageId: id } }, body: { force } });
  unwrap(r);
  refreshBootImages();
  return r.response.status === 202 ? "queued" : "current";
}

export const staleReasonText: Record<NonNullable<BootImage["staleReasons"]>[number], string> = {
  "source-image": "the source image changed",
  "source-index": "the boot.wim index changed",
  architecture: "the architecture changed",
  builder: "the builder changed",
  agent: "the agent was updated",
  "server-urls": "the server addresses changed",
  bootmgr: "the boot manager choice changed",
  tftp: "the TFTP settings changed",
  drivers: "its drivers changed",
  recipe: "the server was updated",
};
