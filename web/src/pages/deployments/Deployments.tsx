import type { UseQueryResult } from "@tanstack/react-query";
import { CircleStop } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import type { Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/form";
import { Alert, PageHeader, td, th } from "@/components/ui/misc";
import { formatDateTime, formatDuration } from "@/lib/format";
import { errorMessage } from "@/lib/problem";
import { clientName, useClients } from "@/pages/clients/data";
import { elapsed, isActive, outcome, statusText, useDeployments, type Deployment, type DeploymentStatus } from "./data";
import { CancelDialog, DeploymentBadge, DeploymentProgress } from "./parts";

export function DeploymentsPage() {
  const [status, setStatus] = useState<DeploymentStatus | "">("");
  const deployments = useDeployments(status ? { status } : {});
  const [cancelling, setCancelling] = useState<Deployment | null>(null);
  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Deployments"
        description="Every install, armed from Clients or started at a machine’s boot prompt. Progress updates as the machine reports it."
      />
      <div className="mb-4">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Show</span>
          <Select className="w-44" value={status} onChange={(e) => setStatus(e.target.value as DeploymentStatus | "")}>
            <option value="">All</option>
            {(Object.keys(statusText) as DeploymentStatus[]).map((s) => (
              <option key={s} value={s}>
                {statusText[s]}
              </option>
            ))}
          </Select>
        </label>
      </div>
      <div className="rounded-lg border bg-card px-5 py-2">
        <DeploymentsTable
          query={deployments}
          showClient
          onCancel={setCancelling}
          empty={status ? "No deployments with this status." : "No deployments yet. Deploy a client from Clients, or start one at a machine’s boot prompt."}
        />
      </div>
      <CancelDialog deployment={cancelling} onClose={() => setCancelling(null)} />
    </div>
  );
}

const originText: Record<NonNullable<Deployment["origin"]>, string> = {
  scheduled: "Armed",
  "boot-prompt": "Boot prompt",
};

/** The deployments table, for this page and a client's history. */
export function DeploymentsTable({
  query,
  showClient,
  onCancel,
  empty,
}: {
  query: UseQueryResult<Schemas["DeploymentPage"]>;
  showClient: boolean;
  onCancel: (d: Deployment) => void;
  empty: string;
}) {
  const clients = useClients();
  if (query.isPending) return <p className="py-3 text-muted-foreground">Loading deployments…</p>;
  if (query.isError)
    return (
      <Alert tone="error" title="Deployments couldn’t be loaded" className="my-3">
        {errorMessage(query.error)}
      </Alert>
    );
  const items = query.data.items;
  if (items.length === 0) return <p className="py-3 text-sm text-muted-foreground">{empty}</p>;
  const client = (id: string) => clients.data?.find((c) => c.id === id);
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b">
            <th scope="col" className={th}>Started</th>
            {showClient && <th scope="col" className={th}>Client</th>}
            <th scope="col" className={th}>Profile</th>
            <th scope="col" className={th}>Status</th>
            <th scope="col" className={th}>Took</th>
            <th scope="col" className={`${th} pr-0 text-right`}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((d) => {
            const c = client(d.clientId);
            const secs = elapsed(d);
            const why = outcome(d);
            return (
              <tr key={d.id} className="border-b last:border-0">
                <td className={td}>
                  <Link to={`/deployments/${d.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                    {formatDateTime(d.startedAt ?? d.createdAt)}
                  </Link>
                  <div className="text-xs text-muted-foreground">{d.origin ? originText[d.origin] : ""}</div>
                </td>
                {showClient && (
                  <td className={td}>
                    {c ? (
                      <Link to={`/clients/${c.id}`} className="underline-offset-4 hover:underline">
                        {clientName(c)}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">Deleted client</span>
                    )}
                    {d.computerName && <div className="text-xs text-muted-foreground">as {d.computerName}</div>}
                  </td>
                )}
                <td className={td}>{d.profileSnapshot?.name ?? "–"}</td>
                <td className={td}>
                  {d.status === "running" ? <DeploymentProgress d={d} /> : <DeploymentBadge d={d} />}
                  {why && d.status === "failed" && <div className="mt-1 max-w-sm text-xs text-muted-foreground">{why}</div>}
                </td>
                <td className={`${td} tabular-nums`}>{secs === null ? "–" : formatDuration(secs)}</td>
                <td className={`${td} pr-0 text-right`}>
                  {isActive(d) && (
                    <Button size="sm" variant="destructive-ghost" aria-label="Cancel this deployment" onClick={() => onCancel(d)}>
                      <CircleStop aria-hidden /> Cancel
                    </Button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
