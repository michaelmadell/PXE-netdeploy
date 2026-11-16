import { ArrowLeft, CircleStop, Download } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/form";
import { Alert, PageHeader, Progress, Section } from "@/components/ui/misc";
import { formatDateTime, formatDuration } from "@/lib/format";
import { ApiError, errorMessage } from "@/lib/problem";
import { cn } from "@/lib/utils";
import { clientName, useClient } from "@/pages/clients/data";
import { useImages } from "@/pages/images/data";
import { elapsed, isActive, outcome, stageText, useDeployment, useDeploymentLogs, type LogEntry } from "./data";
import { CancelDialog, DeploymentBadge } from "./parts";

const identityText = { none: "None (set up at the machine)", local: "Local accounts", ad: "Active Directory", entra: "Microsoft Entra" } as const;

export function DeploymentDetailPage() {
  const { deploymentId = "" } = useParams();
  const deployment = useDeployment(deploymentId);
  const client = useClient(deployment.data?.clientId ?? "");
  const images = useImages();
  const [cancelling, setCancelling] = useState(false);
  const running = deployment.data ? isActive(deployment.data) : false;
  const logs = useDeploymentLogs(deploymentId, running);

  const back = (
    <Link to="/deployments" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Deployments
    </Link>
  );
  if (deployment.isPending) return <p className="text-muted-foreground">Loading deployment…</p>;
  if (deployment.isError) {
    const gone = deployment.error instanceof ApiError && deployment.error.status === 404;
    return (
      <div className="max-w-5xl">
        {back}
        <Alert tone="error" title={gone ? "No such deployment" : "The deployment couldn’t be loaded"}>
          {gone ? "It may have been deleted with its client." : errorMessage(deployment.error)}
        </Alert>
      </div>
    );
  }
  const d = deployment.data;
  const snap = d.profileSnapshot;
  const c = client.data;
  const secs = elapsed(d);
  const why = outcome(d);
  const image = snap ? images.data?.items.find((i) => i.id === snap.installImageId) : undefined;
  const edition =
    snap && image?.details?.kind === "windows" ? image.details.editions.find((e) => e.index === snap.editionIndex)?.name : undefined;

  const facts: [string, ReactNode][] = [
    ["Client", c ? <Link to={`/clients/${c.id}`} className="text-primary underline-offset-4 hover:underline">{clientName(c)}</Link> : "Deleted client"],
    ["Started", d.origin === "scheduled" ? "Armed from this server" : "At the machine’s boot prompt"],
    ["Profile", snap?.name ?? "–"],
    ["Installs", snap ? `${edition ?? `Edition ${snap.editionIndex}`}${image ? ` from ${image.name}` : ""}` : "–"],
    ["Accounts", snap ? identityText[snap.identity.mode] : "–"],
    ["Computer name", d.computerName ?? (snap?.identity.mode === "ad" ? "From the offline join blob, or chosen by Windows" : "Chosen by Windows")],
    ["Armed", formatDateTime(d.createdAt)],
    ["Ran", d.startedAt ? `${formatDateTime(d.startedAt)}${d.finishedAt ? ` to ${formatDateTime(d.finishedAt)}` : ""}` : "Not yet"],
    ["Took", secs === null ? "–" : formatDuration(secs)],
  ];

  return (
    <div className="max-w-5xl">
      {back}
      <PageHeader
        title={`${c ? clientName(c) : "Deployment"} · ${snap?.name ?? ""}`}
        description={<DeploymentBadge d={d} />}
        actions={
          running && (
            <Button variant="destructive-ghost" onClick={() => setCancelling(true)}>
              <CircleStop aria-hidden /> {d.status === "running" ? "Stop" : "Cancel"}
            </Button>
          )
        }
      />
      <div className="space-y-6">
        {d.status === "running" && (
          <Section title={d.stage ? stageText[d.stage] : "Starting"} description={d.message || undefined}>
            <Progress value={d.percent ?? 0} label={`Deployment progress, ${d.percent ?? 0}%`} />
            <p className="mt-2 text-sm tabular-nums text-muted-foreground">{d.percent ?? 0}% of this step</p>
          </Section>
        )}
        {d.status === "pending" && (
          <Alert tone="info" title="Waiting for the machine">
            It installs the next time it network boots.
          </Alert>
        )}
        {why && (
          <Alert tone={d.status === "failed" ? "error" : "info"} title={d.status === "failed" ? "Failed" : "Cancelled"}>
            {why}
          </Alert>
        )}
        {d.status === "succeeded" && (
          <Alert tone="success" title="Installed">
            Windows was applied and the machine restarted into Setup. Setup itself (accounts, domain join) finishes on the machine.
          </Alert>
        )}
        <Section title="Details">
          <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[10rem_1fr]">
            {facts.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 break-words">{v}</dd>
              </div>
            ))}
          </dl>
        </Section>
        <LogSection id={d.id!} entries={logs.data} loading={logs.isPending} error={logs.error} live={running} />
      </div>
      <CancelDialog deployment={cancelling ? d : null} onClose={() => setCancelling(false)} />
    </div>
  );
}

const levelTone: Record<LogEntry["level"], string> = {
  debug: "text-muted-foreground",
  info: "",
  warn: "bg-warning/15",
  error: "text-destructive bg-destructive/10",
};

const time = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

function LogSection({ id, entries, loading, error, live }: { id: string; entries?: LogEntry[]; loading: boolean; error: unknown; live: boolean }) {
  const [level, setLevel] = useState<"all" | "info" | "warn">("info");
  const [search, setSearch] = useState("");
  const shown = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (entries ?? []).filter(
      (e) =>
        (level === "all" || (level === "info" ? e.level !== "debug" : e.level === "warn" || e.level === "error")) &&
        (!q || e.message.toLowerCase().includes(q)),
    );
  }, [entries, level, search]);

  const download = () => {
    const text = (entries ?? []).map((e) => `${e.at}  ${e.level.toUpperCase().padEnd(5)}  ${e.message}`).join("\r\n");
    const url = URL.createObjectURL(new Blob([text + "\r\n"], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `deployment-${id}.log`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Section
      title="Log"
      description={live ? "From the machine, every few seconds while it runs." : "What the machine reported."}
      actions={
        <Button size="sm" disabled={!entries?.length} onClick={download}>
          <Download aria-hidden /> Download
        </Button>
      }
    >
      <div className="mb-3 flex flex-wrap gap-3">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Show</span>
          <Select className="w-48" value={level} onChange={(e) => setLevel(e.target.value as typeof level)}>
            <option value="info">Info and above</option>
            <option value="warn">Warnings and errors</option>
            <option value="all">Everything (with command output)</option>
          </Select>
        </label>
        <label className="flex min-w-60 flex-1 items-center gap-2 text-sm">
          <span className="text-muted-foreground">Find</span>
          <Input type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="e.g. bcdboot" />
        </label>
      </div>
      {loading ? (
        <p className="text-sm text-muted-foreground">Loading the log…</p>
      ) : error ? (
        <Alert tone="error" title="The log couldn’t be loaded">
          {errorMessage(error)}
        </Alert>
      ) : !entries?.length ? (
        <p className="text-sm text-muted-foreground">Nothing yet. The machine sends its log once the deployment starts.</p>
      ) : (
        <>
          <p className="mb-2 text-xs text-muted-foreground">
            {shown.length} of {entries.length} lines
          </p>
          <div className="max-h-[32rem] overflow-auto rounded-md border bg-muted/30 font-mono text-xs">
            <table className="w-full">
              <tbody>
                {shown.map((e, i) => (
                  <tr key={i} className={cn("align-top", levelTone[e.level])}>
                    <td className="py-0.5 pr-3 pl-3 whitespace-nowrap text-muted-foreground tabular-nums">{time.format(new Date(e.at))}</td>
                    <td className="py-0.5 pr-3 whitespace-nowrap uppercase">{e.level}</td>
                    <td className="py-0.5 pr-3 break-all whitespace-pre-wrap">{e.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </Section>
  );
}
