import { useMutation } from "@tanstack/react-query";
import { CircleStop, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/form";
import { Alert, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { errorMessage, unwrap } from "@/lib/problem";
import { refreshDeployments, useActiveDeployments, type Deployment } from "@/pages/deployments/data";
import { ArmDialog, CancelDialog, DeploymentBadge } from "@/pages/deployments/parts";
import { ClientForm } from "./ClientForm";
import { clientError, clientName, refreshClients, sourceText, useClients, useProfiles, type Client, type ClientFilter } from "./data";

export function ClientsPage() {
  const [filter, setFilter] = useState<ClientFilter>({});
  const clients = useClients(filter);
  const profiles = useProfiles();
  const active = useActiveDeployments();
  const [editing, setEditing] = useState<Client | "new" | null>(null);
  const [arming, setArming] = useState<Client | null>(null);
  const [cancelling, setCancelling] = useState<Deployment | null>(null);
  const [deleting, setDeleting] = useState<Client | null>(null);

  const profileName = (id?: string | null) => (id ? profiles.data?.items.find((p) => p.id === id)?.name ?? "Deleted profile" : null);
  const items = clients.data ?? [];
  const filtered = !!(filter.source || filter.profileId);
  const addButton = (
    <Button variant="primary" onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add client
    </Button>
  );

  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Clients"
        description="Machines this server knows: ones you added, and ones discovered when they network booted. Assign a profile, then deploy one remotely: it installs on its next network boot without the boot prompt."
        actions={addButton}
      />
      <div className="mb-4 flex flex-wrap gap-3">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Show</span>
          <Select
            className="w-44"
            value={filter.source ?? ""}
            onChange={(e) => setFilter({ ...filter, source: (e.target.value || undefined) as ClientFilter["source"] })}
          >
            <option value="">All clients</option>
            <option value="manual">Added by hand</option>
            <option value="discovered">Discovered</option>
          </Select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Profile</span>
          <Select className="w-52" value={filter.profileId ?? ""} onChange={(e) => setFilter({ ...filter, profileId: e.target.value || undefined })}>
            <option value="">Any</option>
            {profiles.data?.items.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
        </label>
      </div>

      {clients.isPending ? (
        <p className="text-muted-foreground">Loading clients…</p>
      ) : clients.isError ? (
        <Alert tone="error" title="Clients couldn’t be loaded">
          {errorMessage(clients.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">{filtered ? "No clients match" : "No clients yet"}</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            {filtered
              ? "Change the filters above."
              : "Machines appear here when they network boot from this server, or add one by its MAC address."}
          </p>
          {!filtered && <div className="mt-4 flex justify-center">{addButton}</div>}
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Client</th>
                <th scope="col" className={th}>Profile</th>
                <th scope="col" className={th}>Last seen</th>
                <th scope="col" className={th}>Deployment</th>
                <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.map((c) => {
                const d = active.get(c.id!);
                return (
                  <tr key={c.id} className="border-b last:border-0">
                    <td className={td}>
                      <Link to={`/clients/${c.id}`} className="font-medium text-primary underline-offset-4 hover:underline">
                        {clientName(c)}
                      </Link>
                      <div className="font-mono text-xs text-muted-foreground">{c.mac}</div>
                      {c.source && <div className="text-xs text-muted-foreground">{sourceText[c.source]}</div>}
                    </td>
                    <td className={td}>{profileName(c.profileId) ?? <span className="text-muted-foreground">Default</span>}</td>
                    <td className={td}>
                      {formatDateTime(c.lastSeenAt)}
                      {c.lastIp && <div className="font-mono text-xs text-muted-foreground">{c.lastIp}</div>}
                    </td>
                    <td className={td}>
                      {d ? (
                        <Link to={`/deployments/${d.id}`} className="hover:underline">
                          <DeploymentBadge d={d} />
                        </Link>
                      ) : (
                        <span className="text-muted-foreground">–</span>
                      )}
                    </td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-1">
                        {d ? (
                          <Button size="sm" variant="destructive-ghost" aria-label={`Cancel the deployment of ${clientName(c)}`} onClick={() => setCancelling(d)}>
                            <CircleStop aria-hidden /> Cancel
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" aria-label={`Deploy ${clientName(c)}`} onClick={() => setArming(c)}>
                            <Play aria-hidden /> Deploy
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" aria-label={`Edit ${clientName(c)}`} onClick={() => setEditing(c)}>
                          <Pencil aria-hidden /> Edit
                        </Button>
                        <Button size="sm" variant="destructive-ghost" aria-label={`Delete ${clientName(c)}`} onClick={() => setDeleting(c)}>
                          <Trash2 aria-hidden /> Delete
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <Dialog open={!!editing} onClose={() => setEditing(null)} title={editing === "new" ? "Add client" : "Edit client"}>
        {editing && (
          <ClientForm
            key={editing === "new" ? "new" : editing.etag}
            client={editing === "new" ? null : editing}
            onDone={() => setEditing(null)}
            onCancel={() => setEditing(null)}
          />
        )}
      </Dialog>
      <ArmDialog client={arming} onClose={() => setArming(null)} />
      <CancelDialog deployment={cancelling} onClose={() => setCancelling(null)} />
      <DeleteClientDialog client={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

export function DeleteClientDialog({ client, onClose, onDeleted }: { client: Client | null; onClose: () => void; onDeleted?: () => void }) {
  const del = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/clients/{clientId}", { params: { path: { clientId: id } } })),
    onSuccess: () => {
      refreshClients();
      refreshDeployments();
      onClose();
      onDeleted?.();
    },
  });
  return (
    <Dialog
      open={!!client}
      onClose={() => {
        del.reset();
        onClose();
      }}
      title="Delete client?"
      footer={
        <>
          <Button onClick={onClose}>Keep</Button>
          <Button variant="destructive" disabled={del.isPending} onClick={() => client?.id && del.mutate(client.id)}>
            {del.isPending ? "Deleting…" : "Delete client"}
          </Button>
        </>
      }
    >
      <p className="text-sm">
        <span className="font-medium">{client && clientName(client)}</span> and its deployment history will be removed. Nothing on the machine
        changes; if it network boots again it is discovered again.
      </p>
      {del.isError && (
        <Alert tone="error" className="mt-4">
          {clientError(del.error)}
        </Alert>
      )}
    </Dialog>
  );
}
