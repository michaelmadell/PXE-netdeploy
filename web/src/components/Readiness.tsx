import { useQueries, useQuery } from "@tanstack/react-query";
import { ChevronRight, CircleCheck, CircleDashed, TriangleAlert } from "lucide-react";
import { Link } from "react-router";
import { api, type Schemas } from "@/api/client";
import { Alert, Badge } from "@/components/ui/misc";
import { errorMessage, unwrap } from "@/lib/problem";
import { useBootImages, useSettings, windowsImages } from "@/pages/boot/data";
import { useClients } from "@/pages/clients/data";
import { Findings } from "@/pages/deployments/parts";
import { useImages } from "@/pages/images/data";

type Step = { done: boolean; warn?: boolean; title: string; detail: string; to: string; action: string; findings?: Finding[] };
type Finding = Schemas["ValidationResult"]["findings"][number];
type Validation = Schemas["ValidationResult"];

const validationQuery = (profileId: string) => ({
  queryKey: ["profile-validation", profileId],
  staleTime: 0,
  queryFn: async () => unwrap(await api.POST("/profiles/{profileId}/validate", { params: { path: { profileId } } })),
});

const count = (fs: Finding[], sev: Finding["severity"]) => fs.filter((f) => f.severity === sev).length;
const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

function StatusOf({ v }: { v: Validation | undefined }) {
  if (!v) return <Badge>Checking…</Badge>;
  const errors = count(v.findings, "error");
  const warnings = count(v.findings, "warning");
  if (errors) return <Badge tone="error">{plural(errors, "error")}</Badge>;
  if (warnings) return <Badge tone="warning">{plural(warnings, "warning")}</Badge>;
  return <Badge tone="success">Ready</Badge>;
}

/**
 * The steps between a fresh server and a machine booting from it, then
 * every profile as arming would check it (POST /profiles/{id}/validate).
 */
export function Readiness() {
  const settings = useSettings();
  const images = useImages();
  const bootImages = useBootImages();
  const clients = useClients();
  const profiles = useQuery({
    queryKey: ["profiles"],
    queryFn: async () => unwrap(await api.GET("/profiles", { params: { query: { limit: 200 } } })),
  });
  const items = profiles.data?.items ?? [];
  const checks = useQueries({ queries: items.map((p) => validationQuery(p.id!)) });
  if (!settings.data || !images.data || !bootImages.data || !profiles.data) return null;

  const s = settings.data;
  const answering = s.interfaces.filter((i) => i.services.tftp || i.services.http);
  const ready = windowsImages(images.data.items);
  const built = bootImages.data.items.filter((b) => (b.artifacts?.length ?? 0) > 0);
  const defIndex = items.findIndex((p) => p.id === s.bootPrompt?.defaultProfileId);
  const def = defIndex >= 0 ? items[defIndex] : undefined;
  const defCheck = defIndex >= 0 ? checks[defIndex] : undefined;
  const defV = defCheck?.data;
  const defErrors = defV?.findings.filter((f) => f.severity === "error") ?? [];
  const defWarnings = defV?.findings.filter((f) => f.severity === "warning" && f.code !== "agent_unreachable") ?? [];
  const unreachable = defV?.findings.find((f) => f.code === "agent_unreachable");
  const defBoot = def && bootImages.data.items.find((b) => b.id === def.bootImageId);
  const usedBy = (id: string) => (clients.data ?? []).filter((c) => c.profileId === id).length;

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
      done: !!defV?.ok,
      warn: !!defV && (!defV.ok || defWarnings.length > 0),
      title: "A default profile that can deploy",
      detail: !def
        ? items.length
          ? "Profiles exist, but none is the default, so only registered clients are answered."
          : "Create a profile that pairs the boot image with a Windows edition."
        : defCheck?.isError
          ? `${def.name} couldn’t be checked: ${errorMessage(defCheck.error)}`
          : !defV
            ? `Checking ${def.name}…`
            : !defV.ok
              ? `${def.name} is the default, but deployments with it are refused until ${defErrors.length === 1 ? "this is" : "these are"} fixed:`
              : defWarnings.length
                ? `${def.name}: offered to every machine that network boots. ${plural(defWarnings.length, "warning")}:`
                : `${def.name}: offered to every machine that network boots.`,
      findings: defV ? [...defErrors, ...defWarnings] : undefined,
      to: "/profiles",
      action: "Profiles",
    },
    {
      done: !!defV && !unreachable,
      warn: !!unreachable,
      title: "The booted machine can reach this server",
      detail: !def || !defV
        ? "Checked once there is a default profile."
        : unreachable
          ? unreachable.message
          : defBoot
            ? `The boot image calls ${defBoot.serverUrls.join(", ")}.`
            : "The default profile’s boot image is served.",
      to: "/settings",
      action: "Settings",
    },
  ];
  const all = steps.every((x) => x.done);

  return (
    <>
      <section className="mt-8">
        <h2 className="text-base font-semibold tracking-tight">Ready to network boot?</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          {all
            ? "Yes. Network boot a machine on a deployment network: it starts Windows PE and shows the boot prompt."
            : "Not yet. These are the pieces a machine needs before it can boot from this server."}
        </p>
        <ol className="mt-3 divide-y rounded-lg border bg-card">
          {steps.map((x) => {
            const Icon = x.done && !x.warn ? CircleCheck : x.warn ? TriangleAlert : CircleDashed;
            return (
              <li key={x.title} className="flex items-start gap-3 px-4 py-3 text-sm">
                <Icon
                  className={`mt-0.5 size-4 shrink-0 ${x.done && !x.warn ? "text-success" : x.warn ? "text-warning" : "text-muted-foreground"}`}
                  aria-hidden
                />
                <div className="min-w-0 flex-1">
                  <p className="font-medium">
                    {x.title}
                    <span className="sr-only">{x.done ? (x.warn ? " (done, with warnings)" : " (done)") : " (to do)"}</span>
                  </p>
                  <p className="text-muted-foreground">{x.detail}</p>
                  {x.findings && x.findings.length > 0 && (
                    <ul className="mt-1.5 list-disc space-y-0.5 pl-5">
                      {x.findings.map((f, i) => (
                        <li key={i} className={f.severity === "error" ? "text-destructive" : "text-foreground"}>
                          {f.message}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
                {(!x.done || x.warn) && (
                  <Link to={x.to} className="shrink-0 text-primary underline underline-offset-4">
                    {x.action}
                  </Link>
                )}
              </li>
            );
          })}
        </ol>
      </section>

      {items.length > 0 && (
        <section className="mt-8">
          <h2 className="text-base font-semibold tracking-tight">Profiles</h2>
          <p className="mt-1 text-sm text-muted-foreground">Each profile as deploying would check it. Open one to see what it found.</p>
          <ul className="mt-3 divide-y rounded-lg border bg-card">
            {items.map((p, i) => {
              const c = checks[i];
              const n = usedBy(p.id!);
              return (
                <li key={p.id}>
                  <details className="group">
                    <summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-3 text-sm [&::-webkit-details-marker]:hidden">
                      <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" aria-hidden />
                      <span className="min-w-0 flex-1">
                        <span className="font-medium">{p.name}</span>
                        <span className="ml-2 text-muted-foreground">
                          {p.id === s.bootPrompt?.defaultProfileId ? "Default · " : ""}
                          {n === 0 ? "no clients" : plural(n, "client")}
                        </span>
                      </span>
                      {c?.isError ? <Badge tone="error">Couldn’t check</Badge> : <StatusOf v={c?.data} />}
                    </summary>
                    <div className="px-4 pb-4 pl-11">
                      {c?.isError ? (
                        <Alert tone="error">{errorMessage(c.error)}</Alert>
                      ) : c?.data ? (
                        <Findings findings={c.data.findings} />
                      ) : (
                        <p className="text-sm text-muted-foreground">Checking…</p>
                      )}
                      <Link to="/profiles" className="mt-3 inline-block text-sm text-primary underline underline-offset-4">
                        Edit profiles
                      </Link>
                    </div>
                  </details>
                </li>
              );
            })}
          </ul>
        </section>
      )}
    </>
  );
}
