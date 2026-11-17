import { useMutation } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, FieldErrors, Input } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { useBootImages, useDriverGroups } from "@/pages/boot/data";
import { useProfiles } from "@/pages/clients/data";
import {
  contents,
  driverError,
  groupUsers,
  refreshDrivers,
  signatureTone,
  signingSummary,
  useDriverPackages,
  type DriverGroup,
} from "./data";

export function DriverGroupsPage() {
  const groups = useDriverGroups();
  const packages = useDriverPackages();
  const bootImages = useBootImages();
  const profiles = useProfiles();
  const [editing, setEditing] = useState<DriverGroup | "new" | null>(null);
  const [deleting, setDeleting] = useState<DriverGroup | null>(null);
  const del = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/driver-groups/{driverGroupId}", { params: { path: { driverGroupId: id } } })),
    onSuccess: () => {
      refreshDrivers();
      setDeleting(null);
    },
  });

  const items = groups.data?.items ?? [];
  const pkgName = (id: string) => packages.data?.find((p) => p.id === id)?.name ?? "Deleted package";
  const users = (g: DriverGroup) => groupUsers(g.id!, bootImages.data?.items, profiles.data?.items);
  const deletingUsers = deleting ? users(deleting) : null;
  const deletingBlocked = !!deletingUsers && deletingUsers.bootImages.length + deletingUsers.profiles.length > 0;
  const add = (
    <Button variant="primary" onClick={() => setEditing("new")}>
      <Plus aria-hidden /> New group
    </Button>
  );

  return (
    <div className="max-w-6xl">
      <PageHeader
        title="Driver groups"
        description="A group is a set of driver packages used together. Boot images take groups for the drivers WinPE needs to reach the network and the disk; profiles take groups for the drivers installed into Windows."
        actions={add}
      />
      <Alert tone="info" className="mb-6">
        Storage drivers (PERC, Intel RST or VMD) belong in both: in the boot image’s groups so the agent can see the disk, and in the
        profile’s so Windows can start from it.
      </Alert>

      {groups.isPending ? (
        <p className="text-muted-foreground">Loading driver groups…</p>
      ) : groups.isError ? (
        <Alert tone="error" title="Driver groups couldn’t be loaded">
          {errorMessage(groups.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No driver groups yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Import driver packages first, then group them, for example “Dell OptiPlex network” or “PERC storage”.
          </p>
          <div className="mt-4 flex justify-center">{add}</div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Packages</th>
                <th scope="col" className={th}>Used by</th>
                <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.map((g) => {
                const u = users(g);
                return (
                  <tr key={g.id} className="border-b last:border-0">
                    <td className={td}>
                      <span className="font-medium">{g.name}</span>
                      {g.description && <div className="text-xs text-muted-foreground">{g.description}</div>}
                    </td>
                    <td className={td}>
                      {g.driverPackageIds.length === 0 ? (
                        <span className="text-muted-foreground">Empty</span>
                      ) : (
                        <ul className="space-y-0.5">
                          {g.driverPackageIds.map((id) => (
                            <li key={id}>
                              <Link to={`/drivers/${id}`} className="text-primary underline-offset-4 hover:underline">
                                {pkgName(id)}
                              </Link>
                            </li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td className={td}>
                      {u.bootImages.length + u.profiles.length === 0 ? (
                        <span className="text-muted-foreground">Nothing</span>
                      ) : (
                        <ul className="space-y-0.5">
                          {u.bootImages.map((b) => (
                            <li key={b.id}>
                              Boot image{" "}
                              <Link to={`/boot-images/${b.id}`} className="text-primary underline-offset-4 hover:underline">
                                {b.name}
                              </Link>
                            </li>
                          ))}
                          {u.profiles.map((p) => (
                            <li key={p.id}>Profile {p.name}</li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" aria-label={`Edit ${g.name}`} onClick={() => setEditing(g)}>
                          <Pencil aria-hidden /> Edit
                        </Button>
                        <Button size="sm" variant="destructive-ghost" aria-label={`Delete ${g.name}`} onClick={() => { del.reset(); setDeleting(g); }}>
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

      <Dialog wide open={!!editing} onClose={() => setEditing(null)} title={editing === "new" ? "New driver group" : "Edit driver group"}>
        {editing && (
          <GroupForm
            key={editing === "new" ? "new" : editing.etag}
            group={editing === "new" ? null : editing}
            usedByBootImages={editing === "new" ? [] : users(editing).bootImages.map((b) => b.name)}
            onDone={() => {
              refreshDrivers();
              setEditing(null);
            }}
            onCancel={() => setEditing(null)}
          />
        )}
      </Dialog>

      <Dialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title="Delete driver group?"
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending || deletingBlocked} onClick={() => deleting && del.mutate(deleting.id!)}>
              {del.isPending ? "Deleting…" : "Delete group"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          {deletingBlocked ? (
            <>
              <span className="font-medium">{deleting?.name}</span> is used by{" "}
              {[...deletingUsers!.bootImages.map((b) => `boot image ${b.name}`), ...deletingUsers!.profiles.map((p) => `profile ${p.name}`)].join(", ")}.
              Remove it from them first.
            </>
          ) : (
            <>
              <span className="font-medium">{deleting?.name}</span> will be removed. Its driver packages are kept.
            </>
          )}
        </p>
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {driverError(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}

function GroupForm({
  group,
  usedByBootImages,
  onDone,
  onCancel,
}: {
  group: DriverGroup | null;
  usedByBootImages: string[];
  onDone: () => void;
  onCancel: () => void;
}) {
  const packages = useDriverPackages();
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [ids, setIds] = useState<string[]>(group?.driverPackageIds ?? []);
  const all = packages.data ?? [];
  const chosen = all.filter((p) => ids.includes(p.id!));
  const unsigned = chosen.filter((p) => p.infs.some((i) => i.enabled && i.signature === "unsigned"));
  const changedMembers = !!group && (ids.length !== group.driverPackageIds.length || ids.some((x) => !group.driverPackageIds.includes(x)));

  const save = useMutation({
    mutationFn: async () => {
      const body = { name: name.trim(), description: description.trim(), driverPackageIds: ids };
      return group
        ? unwrap(await api.PUT("/driver-groups/{driverGroupId}", { params: { path: { driverGroupId: group.id! }, header: { "If-Match": group.etag ?? "*" } }, body }))
        : unwrap(await api.POST("/driver-groups", { body }));
    },
    onSuccess: onDone,
  });

  return (
    <FieldErrors value={save.error instanceof ApiError ? save.error.fields : []}>
      <form
        className="space-y-5"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) save.mutate();
        }}
      >
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Name" path="name">
            {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="Description" path="description">
            {(p) => <Input {...p} value={description} onChange={(e) => setDescription(e.target.value)} />}
          </Field>
        </div>
        <fieldset>
          <legend className="mb-1.5 text-sm font-medium">Packages</legend>
          {packages.isPending ? (
            <p className="text-sm text-muted-foreground">Loading packages…</p>
          ) : all.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No driver packages yet. <Link to="/drivers" className="text-primary underline-offset-4 hover:underline">Add one</Link> first.
            </p>
          ) : (
            <div className="space-y-3">
              {all.map((p) => {
                const sign = signingSummary(p);
                const ready = p.status === "ready";
                return (
                  <Checkbox
                    key={p.id}
                    label={
                      <>
                        {p.name} {ready && sign && <Badge tone={signatureTone[sign.sig]}>{sign.text}</Badge>}
                      </>
                    }
                    hint={ready ? contents(p) : p.status === "failed" ? "Import failed: can’t be used" : "Still importing"}
                    checked={ids.includes(p.id!)}
                    disabled={!ready && !ids.includes(p.id!)}
                    onChange={(on) => setIds(on ? [...ids, p.id!] : ids.filter((x) => x !== p.id))}
                  />
                );
              })}
            </div>
          )}
        </fieldset>
        {unsigned.length > 0 && (
          <Alert tone="warning" title="Unsigned drivers">
            {unsigned.map((p) => p.name).join(", ")} {unsigned.length === 1 ? "has" : "have"} unsigned drivers enabled. A boot image with this group
            won’t build until they are disabled on the package’s page.
          </Alert>
        )}
        {changedMembers && usedByBootImages.length > 0 && (
          <Alert tone="info">{usedByBootImages.join(", ")} will be out of date and need building again.</Alert>
        )}
        {save.isError && (
          <Alert tone="error">
            {save.error instanceof ApiError && save.error.status === 412 ? "This group was changed somewhere else. Close and try again." : driverError(save.error)}
          </Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim() || save.isPending}>
            {save.isPending ? "Saving…" : group ? "Save" : "Create group"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}
