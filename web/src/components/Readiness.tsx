import { useQuery } from "@tanstack/react-query";
import { CircleCheck, CircleDashed, TriangleAlert } from "lucide-react";
import { Link } from "react-router";
import { api } from "@/api/client";
import { unwrap } from "@/lib/problem";
import { useBootImages, useSettings, windowsImages } from "@/pages/boot/data";
import { useImages } from "@/pages/images/data";

type Step = { done: boolean; warn?: boolean; title: string; detail: string; to: string; action: string };

/** The port a URL names, with the scheme's default when it names none. */
function portOf(url: string): number | null {
  try {
    const u = new URL(url);
    return u.port ? Number(u.port) : u.protocol === "https:" ? 443 : 80;
  } catch {
    return null;
  }
}

/**
 * The steps between a fresh server and a machine booting from it, each
 * ticked from what the server has now.
 */
export function Readiness() {
  const settings = useSettings();
  const images = useImages();
  const bootImages = useBootImages();
  const profiles = useQuery({
    queryKey: ["profiles"],
    queryFn: async () => unwrap(await api.GET("/profiles", { params: { query: { limit: 200 } } })),
  });
  if (!settings.data || !images.data || !bootImages.data || !profiles.data) return null;

  const s = settings.data;
  const answering = s.interfaces.filter((i) => i.services.tftp || i.services.http);
  const ready = windowsImages(images.data.items);
  const built = bootImages.data.items.filter((b) => (b.artifacts?.length ?? 0) > 0);
  const def = profiles.data.items.find((p) => p.id === s.bootPrompt?.defaultProfileId);
  const defBoot = def && bootImages.data.items.find((b) => b.id === def.bootImageId);
  const defBuilt = !!defBoot && (defBoot.artifacts?.length ?? 0) > 0;
  // The agent in WinPE calls back to the boot image's server addresses: some interface must listen on one of those ports.
  const httpPorts = new Set(s.interfaces.filter((i) => i.services.http).map((i) => i.services.httpPort));
  const reachable = !!defBoot && defBoot.serverUrls.some((u) => httpPorts.has(portOf(u) ?? -1));

  const steps: Step[] = [
    {
      done: answering.length > 0,
      title: "A deployment interface",
      detail: answering.length ? answering.map((i) => `${i.name} (DHCP ${i.dhcp.mode})`).join(", ") : "No network adapter is set up to answer machines yet.",
      to: "/settings",
      action: "Settings",
    },
    {
      done: ready.length > 0,
      title: "A Windows image",
      detail: ready.length ? ready.map((r) => r.image.name).join(", ") : "Upload a Windows ISO.",
      to: "/images",
      action: "Images",
    },
    {
      done: built.length > 0,
      warn: built.some((b) => b.status === "stale"),
      title: "A built boot image",
      detail: built.length
        ? built.map((b) => b.name + (b.status === "stale" ? " (out of date)" : "")).join(", ")
        : bootImages.data.items.length
          ? "A boot image exists but hasn’t been built."
          : "Build Windows PE from the Windows image.",
      to: "/boot-images",
      action: "Boot images",
    },
    {
      done: !!def && defBuilt,
      title: "A default profile",
      detail: def
        ? defBuilt
          ? `${def.name}: offered to every machine that network boots.`
          : `${def.name} is the default, but its boot image isn’t built.`
        : profiles.data.items.length
          ? "Profiles exist, but none is the default, so only registered clients are answered."
          : "Create a profile that pairs the boot image with a Windows edition.",
      to: "/profiles",
      action: "Profiles",
    },
    {
      done: reachable,
      warn: !!defBoot && !reachable,
      title: "The booted machine can reach this server",
      detail: !defBoot
        ? "Checked once there is a default profile."
        : reachable
          ? `The boot image calls ${defBoot.serverUrls.join(", ")}.`
          : `The boot image calls ${defBoot.serverUrls.join(", ")}, but no deployment interface has HTTP on that port. Machines will boot, then fail to find the server.`,
      to: "/settings",
      action: "Settings",
    },
  ];
  const all = steps.every((x) => x.done);

  return (
    <section className="mt-8">
      <h2 className="text-base font-semibold tracking-tight">Ready to network boot?</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {all
          ? "Yes. Network boot a machine on a deployment network: it starts Windows PE and shows the boot prompt."
          : "Not yet. These are the pieces a machine needs before it can boot from this server."}
      </p>
      <ol className="mt-3 divide-y rounded-lg border bg-card">
        {steps.map((x) => {
          const Icon = x.done ? CircleCheck : x.warn ? TriangleAlert : CircleDashed;
          return (
            <li key={x.title} className="flex items-start gap-3 px-4 py-3 text-sm">
              <Icon className={`mt-0.5 size-4 shrink-0 ${x.done ? "text-success" : x.warn ? "text-warning" : "text-muted-foreground"}`} aria-hidden />
              <div className="min-w-0 flex-1">
                <p className="font-medium">
                  {x.title}
                  <span className="sr-only">{x.done ? " (done)" : " (to do)"}</span>
                </p>
                <p className="text-muted-foreground">{x.detail}</p>
              </div>
              {!x.done && (
                <Link to={x.to} className="shrink-0 text-primary underline underline-offset-4">
                  {x.action}
                </Link>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
