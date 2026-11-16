import { ArrowLeft, CircleStop, Pencil, Play, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Alert, Dialog, PageHeader, Section } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { ApiError, errorMessage } from "@/lib/problem";
import { isActive, useDeployments, type Deployment } from "@/pages/deployments/data";
import { DeploymentsTable } from "@/pages/deployments/Deployments";
import { ArmDialog, CancelDialog } from "@/pages/deployments/parts";
import { ClientForm } from "./ClientForm";
import { DeleteClientDialog } from "./Clients";
import { clientName, sourceText, useClient, useProfiles, useSecrets } from "./data";

export function ClientDetailPage() {
  const { clientId = "" } = useParams();
  const navigate = useNavigate();
  const client = useClient(clientId);
  const history = useDeployments({ clientId });
  const profiles = useProfiles();
  const blobs = useSecrets("odj-blob");
  const [editing, setEditing] = useState(false);
  const [arming, setArming] = useState(false);
  const [cancelling, setCancelling] = useState<Deployment | null>(null);
  const [deleting, setDeleting] = useState(false);

  const back = (
    <Link to="/clients" className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Clients
    </Link>
  );
  if (client.isPending) return <p className="text-muted-foreground">Loading client…</p>;
  if (client.isError) {
    const gone = client.error instanceof ApiError && client.error.status === 404;
    return (
      <div className="max-w-5xl">
        {back}
        <Alert tone="error" title={gone ? "No such client" : "The client couldn’t be loaded"}>
          {gone ? "It may have been deleted." : errorMessage(client.error)}
        </Alert>
      </div>
    );
  }
  const c = client.data;
  const active = history.data?.items.find(isActive);
  const profile = c.profileId ? profiles.data?.items.find((p) => p.id === c.profileId) : undefined;
  const blob = c.secretRefs?.odjBlob ? blobs.data?.items.find((s) => s.id === c.secretRefs?.odjBlob) : undefined;
  const vars = Object.entries(c.variables ?? {});

  const facts: [string, ReactNode][] = [
    ["MAC address", <span className="font-mono">{c.mac}</span>],
    ["Profile", c.profileId ? (profile?.name ?? "Deleted profile") : "None (the default profile)"],
    ["Serial number", c.serial || "–"],
    ["SMBIOS UUID", c.smbiosUuid ? <span className="font-mono">{c.smbiosUuid}</span> : "–"],
    ["Reserved address", c.reservedIp ? <span className="font-mono">{c.reservedIp}</span> : "–"],
    ["Last seen", c.lastSeenAt ? `${formatDateTime(c.lastSeenAt)}${c.lastIp ? ` at ${c.lastIp}` : ""}` : "Never"],
    ["Firmware", c.lastArch ?? "–"],
    ["Offline join blob", c.secretRefs?.odjBlob ? (blob?.name ?? "A secret") : "–"],
    ["Variables", vars.length ? <span className="font-mono">{vars.map(([k, v]) => `${k}=${v}`).join(", ")}</span> : "–"],
    ["Added", `${c.source ? sourceText[c.source] : ""} · ${formatDateTime(c.createdAt)}`],
  ];

  return (
    <div className="max-w-5xl">
      {back}
      <PageHeader
        title={clientName(c)}
        description={c.hostname || c.serial ? c.mac : undefined}
        actions={
          <>
            {active ? (
              <Button variant="destructive-ghost" onClick={() => setCancelling(active)}>
                <CircleStop aria-hidden /> Cancel deployment
              </Button>
            ) : (
              <Button variant="primary" onClick={() => setArming(true)}>
                <Play aria-hidden /> Deploy
              </Button>
            )}
            <Button onClick={() => setEditing(true)}>
              <Pencil aria-hidden /> Edit
            </Button>
            <Button variant="destructive-ghost" onClick={() => setDeleting(true)}>
              <Trash2 aria-hidden /> Delete
            </Button>
          </>
        }
      />
      <div className="space-y-6">
        <Section title="Details">
          <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
            {facts.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 break-words">{v}</dd>
              </div>
            ))}
          </dl>
        </Section>
        <Section title="Deployments" description="Newest first. Open one for its progress and log.">
          <DeploymentsTable query={history} showClient={false} onCancel={setCancelling} empty="This client hasn’t been deployed yet." />
        </Section>
      </div>

      <Dialog open={editing} onClose={() => setEditing(false)} title="Edit client">
        {editing && <ClientForm key={c.etag} client={c} onDone={() => setEditing(false)} onCancel={() => setEditing(false)} />}
      </Dialog>
      <ArmDialog client={arming ? c : null} onClose={() => setArming(false)} />
      <CancelDialog deployment={cancelling} onClose={() => setCancelling(null)} />
      <DeleteClientDialog client={deleting ? c : null} onClose={() => setDeleting(false)} onDeleted={() => void navigate("/clients")} />
    </div>
  );
}
