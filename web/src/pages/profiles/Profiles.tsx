import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Star, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, FieldErrors, Input, RadioCards, Select } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { useBootImages, useSettings, windowsImages } from "@/pages/boot/data";
import { useImages } from "@/pages/images/data";

type Profile = Schemas["Profile"];
type ProfileInput = Schemas["ProfileInput"];

const identityText: Record<Schemas["IdentityMode"], string> = {
  none: "Set up by the person at the machine",
  local: "Local accounts",
  ad: "Active Directory join",
  entra: "Microsoft Entra",
};

const codes: Record<string, string> = {
  unknown_boot_image: "That boot image no longer exists.",
  install_image_unavailable: "That Windows image is missing or not ready.",
  architecture_mismatch: "The boot image and the Windows image are for different architectures.",
  unknown_edition: "The Windows image has no such edition.",
  profile_in_use: "This profile is the default, or a client is assigned to it. Change those first.",
};
const message = (e: unknown) => (e instanceof ApiError && codes[e.code]) || errorMessage(e);

/** Reads settings fresh and sets the profile offered to machines that have none of their own. */
async function setDefaultProfile(id: string | null) {
  const cur = unwrap(await api.GET("/settings"));
  return unwrap(
    await api.PUT("/settings", {
      params: { header: { "If-Match": cur.etag ?? "*" } },
      body: { ...cur, bootPrompt: { ...cur.bootPrompt!, defaultProfileId: id } },
    }),
  );
}

export function ProfilesPage() {
  const qc = useQueryClient();
  const profiles = useQuery({
    queryKey: ["profiles"],
    queryFn: async () => unwrap(await api.GET("/profiles", { params: { query: { limit: 200 } } })),
  });
  const settings = useSettings();
  const bootImages = useBootImages();
  const images = useImages();
  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [deleting, setDeleting] = useState<Profile | null>(null);

  const refresh = () => {
    for (const key of ["profiles", "settings", "health"]) void qc.invalidateQueries({ queryKey: [key] });
  };
  const makeDefault = useMutation({ mutationFn: setDefaultProfile, onSuccess: refresh });
  const del = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/profiles/{profileId}", { params: { path: { profileId: id } } })),
    onSuccess: () => {
      refresh();
      setDeleting(null);
    },
  });

  const items = profiles.data?.items ?? [];
  const defaultId = settings.data?.bootPrompt?.defaultProfileId ?? null;
  const bootImage = (id: string) => bootImages.data?.items.find((b) => b.id === id);
  const edition = (p: Profile) => {
    const im = images.data?.items.find((i) => i.id === p.installImageId);
    const ed = im?.details?.kind === "windows" ? im.details.editions.find((e) => e.index === p.editionIndex) : undefined;
    return { image: im?.name ?? "Deleted image", edition: ed?.name ?? `Edition ${p.editionIndex}` };
  };
  const newButton = (
    <Button variant="primary" onClick={() => setEditing("new")}>
      <Plus aria-hidden /> New profile
    </Button>
  );

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Profiles"
        description="A profile is what a machine can be turned into: the boot image it starts and the Windows edition it installs. The default profile is offered to any machine that network boots."
        actions={newButton}
      />

      {makeDefault.isError && (
        <Alert tone="error" className="mb-6" title="The default profile wasn’t changed">
          {message(makeDefault.error)}
        </Alert>
      )}
      {items.length > 0 && settings.data && !defaultId && (
        <Alert tone="warning" className="mb-6" title="No default profile">
          Machines that aren’t registered as clients get no answer when they network boot. Make one of these the default.
        </Alert>
      )}

      {profiles.isPending ? (
        <p className="text-muted-foreground">Loading profiles…</p>
      ) : profiles.isError ? (
        <Alert tone="error" title="Profiles couldn’t be loaded">
          {errorMessage(profiles.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <p className="font-medium">No profiles yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            You need a built boot image and a Windows image first. A profile joins the two, and machines boot the profile.
          </p>
          <div className="mt-4 flex justify-center">{newButton}</div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Boot image</th>
                <th scope="col" className={th}>Installs</th>
                <th scope="col" className={th}>Accounts</th>
                <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.map((p) => {
                const b = bootImage(p.bootImageId);
                const built = (b?.artifacts?.length ?? 0) > 0;
                const e = edition(p);
                return (
                  <tr key={p.id} className="border-b last:border-0">
                    <td className={td}>
                      <span className="font-medium">{p.name}</span>{" "}
                      {p.id === defaultId && <Badge tone="success">Default</Badge>}
                      {p.description && <div className="text-xs text-muted-foreground">{p.description}</div>}
                    </td>
                    <td className={td}>
                      {b ? (
                        <Link to={`/boot-images/${b.id}`} className="text-primary underline-offset-4 hover:underline">
                          {b.name}
                        </Link>
                      ) : (
                        "Deleted boot image"
                      )}
                      {b && !built && (
                        <div className="mt-1">
                          <Badge tone="warning">Not built: can’t boot yet</Badge>
                        </div>
                      )}
                    </td>
                    <td className={td}>
                      {e.edition}
                      <div className="text-xs text-muted-foreground">{e.image}</div>
                    </td>
                    <td className={td}>{identityText[p.identity.mode]}</td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-1">
                        {p.id !== defaultId && (
                          <Button size="sm" variant="ghost" disabled={makeDefault.isPending} aria-label={`Make ${p.name} the default`} onClick={() => makeDefault.mutate(p.id)}>
                            <Star aria-hidden /> Make default
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" aria-label={`Edit ${p.name}`} onClick={() => setEditing(p)}>
                          <Pencil aria-hidden /> Edit
                        </Button>
                        <Button size="sm" variant="destructive-ghost" aria-label={`Delete ${p.name}`} onClick={() => { del.reset(); setDeleting(p); }}>
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

      <Dialog open={!!editing} onClose={() => setEditing(null)} title={editing === "new" ? "New profile" : "Edit profile"}>
        {editing && (
          <ProfileForm
            key={editing === "new" ? "new" : editing.etag}
            profile={editing === "new" ? null : editing}
            offerDefault={!defaultId}
            onDone={() => {
              refresh();
              setEditing(null);
            }}
            onCancel={() => setEditing(null)}
          />
        )}
      </Dialog>

      <Dialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title="Delete profile?"
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending} onClick={() => deleting && del.mutate(deleting.id)}>
              {del.isPending ? "Deleting…" : "Delete profile"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          <span className="font-medium">{deleting?.name}</span> will be removed. Its boot image and Windows image are kept.
        </p>
        {del.isError && (
          <Alert tone="error" className="mt-4">
            {message(del.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}

function ProfileForm({
  profile,
  offerDefault,
  onDone,
  onCancel,
}: {
  profile: Profile | null;
  /** No default profile is set, so offer to make this one it. */
  offerDefault: boolean;
  onDone: () => void;
  onCancel: () => void;
}) {
  const bootImages = useBootImages();
  const images = useImages();
  const sources = windowsImages(images.data?.items);
  const boots = bootImages.data?.items ?? [];

  const [name, setName] = useState(profile?.name ?? "");
  const [bootImageId, setBootImageId] = useState(profile?.bootImageId ?? (boots.length === 1 ? boots[0]!.id : ""));
  const [installImageId, setInstallImageId] = useState(profile?.installImageId ?? (sources.length === 1 ? sources[0]!.image.id : ""));
  const [editionIndex, setEditionIndex] = useState<number | null>(profile?.editionIndex ?? null);
  const [bootOrder, setBootOrder] = useState(profile?.bootOrderAfterDeploy ?? "disk-first");
  const [asDefault, setAsDefault] = useState(offerDefault);

  const boot = boots.find((b) => b.id === bootImageId);
  const source = sources.find((s) => s.image.id === installImageId);
  const mismatch = boot && source && boot.architecture !== source.win.architecture;
  const valid = !!name.trim() && !!bootImageId && !!installImageId && editionIndex !== null && !mismatch;

  const save = useMutation({
    mutationFn: async () => {
      // Everything this form doesn't show (identity, variables, OS settings) is kept as it was.
      const { id: _id, etag: _etag, createdAt: _c, updatedAt: _u, ...kept } = profile ?? ({} as Partial<Profile>);
      const input: ProfileInput = {
        installMethod: "agent",
        identity: { mode: "none", skipEula: true },
        ...kept,
        name: name.trim(),
        bootImageId,
        installImageId,
        editionIndex: editionIndex!,
        bootOrderAfterDeploy: bootOrder,
      };
      const saved = profile
        ? unwrap(await api.PUT("/profiles/{profileId}", { params: { path: { profileId: profile.id }, header: { "If-Match": profile.etag } }, body: input }))
        : unwrap(await api.POST("/profiles", { body: input }));
      if (asDefault) await setDefaultProfile(saved.id);
      return saved;
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
          if (valid) save.mutate();
        }}
      >
        <Field label="Name" path="name" hint="Shown in the menu on the machine, for example “Windows 11 Pro”.">
          {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field
          label="Boot image"
          path="bootImageId"
          hint={boots.length === 0 ? "No boot images yet. Make one on the Boot images page first." : boot && !(boot.artifacts?.length) ? "This boot image isn’t built yet, so machines can’t boot this profile until it is." : undefined}
        >
          {(p) => (
            <Select {...p} value={bootImageId} onChange={(e) => setBootImageId(e.target.value)}>
              <option value="" disabled>
                Choose a boot image…
              </option>
              {boots.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name} · {b.architecture}
                  {b.artifacts?.length ? "" : " · not built"}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Windows image" path="installImageId" hint={mismatch ? undefined : "What gets installed."}>
            {(p) => (
              <Select
                {...p}
                value={installImageId}
                onChange={(e) => {
                  setInstallImageId(e.target.value);
                  setEditionIndex(null);
                }}
              >
                <option value="" disabled>
                  Choose an image…
                </option>
                {sources.map(({ image, win }) => (
                  <option key={image.id} value={image.id}>
                    {image.name} · {win.architecture}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Edition" path="editionIndex">
            {(p) => (
              <Select {...p} disabled={!source} value={editionIndex ?? ""} onChange={(e) => setEditionIndex(Number(e.target.value))}>
                <option value="" disabled>
                  Choose an edition…
                </option>
                {source?.win.editions.map((ed) => (
                  <option key={ed.index} value={ed.index}>
                    {ed.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
        {mismatch && (
          <Alert tone="error">
            The boot image is {boot.architecture} and the Windows image is {source.win.architecture}. Choose a matching pair.
          </Alert>
        )}

        <RadioCards
          legend="After installing, the machine starts from"
          value={bootOrder}
          onChange={setBootOrder}
          options={[
            { value: "disk-first", label: "Its disk", description: "Normal for most machines. Network boot again from the firmware’s boot menu." },
            { value: "network-first", label: "The network", description: "Every start shows the boot prompt first, then continues to the disk." },
          ]}
        />

        {profile && profile.identity.mode !== "none" ? (
          <p className="text-sm text-muted-foreground">Accounts: {identityText[profile.identity.mode]} (set through the API; kept as it is).</p>
        ) : (
          <p className="text-sm text-muted-foreground">
            Windows is installed without accounts: whoever first starts the machine goes through Windows setup.
          </p>
        )}

        {(offerDefault || asDefault) && (
          <Checkbox
            label="Make this the default profile"
            hint="Offered to every machine that network boots and has no profile of its own."
            checked={asDefault}
            onChange={setAsDefault}
          />
        )}

        {save.isError && <Alert tone="error">{message(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!valid || save.isPending}>
            {save.isPending ? "Saving…" : profile ? "Save" : "Create profile"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}
