import { useMutation, useQuery } from "@tanstack/react-query";
import { KeyRound, Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { Button } from "@/components/ui/button";
import { Field, FieldErrors, Input, Select } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, td, th } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { uploadSmall } from "@/lib/tus";

type Secret = Schemas["Secret"];
type Kind = Schemas["SecretKind"];
type Value = Schemas["SecretValue"];

export const kindText: Record<Kind, string> = {
  password: "Password",
  "product-key": "Product key",
  "domain-credential": "Domain join account",
  "share-credential": "Share account",
  "odj-blob": "Offline domain join blob",
  "autopilot-json": "Autopilot profile",
  "provisioning-package": "Provisioning package",
};

const kindHelp: Record<Kind, string> = {
  password: "For local accounts a profile creates.",
  "product-key": "Entered during Setup; five groups of five.",
  "domain-credential": "Joins machines to Active Directory during Setup. The account needs rights to create computers in the OU.",
  "share-credential": "For the backup destination and SMB shares.",
  "odj-blob": "The file from djoin /provision /savefile, for one machine. Attach it to that client.",
  "autopilot-json": "AutopilotConfigurationFile.json (Phase 5).",
  "provisioning-package": "A .ppkg (Phase 5).",
};

const credentialKinds: Kind[] = ["domain-credential", "share-credential"];
const fileKinds: Kind[] = ["odj-blob", "autopilot-json", "provisioning-package"];
/** Kinds offered when creating; the Phase 5 ones aren't used by anything yet. */
const creatable: Kind[] = ["password", "product-key", "domain-credential", "share-credential", "odj-blob"];

export function refreshSecrets() {
  void queryClient.invalidateQueries({ queryKey: ["secrets"] });
  void queryClient.invalidateQueries({ queryKey: ["health"] });
}

const codes: Record<string, string> = {
  secret_in_use: "Something still uses this secret.",
};
const message = (e: unknown) => (e instanceof ApiError && e.code === "secret_in_use" ? `${codes.secret_in_use} ${e.message}` : errorMessage(e));

export function SecretsPage() {
  const secrets = useQuery({
    queryKey: ["secrets", "all"],
    queryFn: async () => unwrap(await api.GET("/secrets", { params: { query: { limit: 200 } } })),
  });
  const [creating, setCreating] = useState(false);
  const [replacing, setReplacing] = useState<Secret | null>(null);
  const [renaming, setRenaming] = useState<Secret | null>(null);
  const [deleting, setDeleting] = useState<Secret | null>(null);
  const del = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/secrets/{secretId}", { params: { path: { secretId: id } } })),
    onSuccess: () => {
      refreshSecrets();
      setDeleting(null);
    },
  });
  const items = secrets.data?.items ?? [];
  const add = (
    <Button variant="primary" onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Add secret
    </Button>
  );

  return (
    <div className="max-w-5xl">
      <PageHeader
        title="Secrets"
        description="Passwords, join accounts and files that profiles and clients use. Values are encrypted on this server and never shown again; a deployment gets its own copy when it starts, so changing one later doesn’t affect it."
        actions={add}
      />
      <Alert tone="info" className="mb-6" title="Keep a copy of the secrets key">
        Backups don’t include the key that encrypts these. Run <code className="font-mono">netdeployd key export</code> once and keep the file
        somewhere safe, or secrets can’t be restored on other hardware.
      </Alert>

      {secrets.isPending ? (
        <p className="text-muted-foreground">Loading secrets…</p>
      ) : secrets.isError ? (
        <Alert tone="error" title="Secrets couldn’t be loaded">
          {errorMessage(secrets.error)}
        </Alert>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed px-6 py-12 text-center">
          <KeyRound className="mx-auto size-6 text-muted-foreground" aria-hidden />
          <p className="mt-2 font-medium">No secrets yet</p>
          <p className="mx-auto mt-1 max-w-prose text-sm text-muted-foreground">
            Add a password before giving a profile local accounts, or a join account for Active Directory.
          </p>
          <div className="mt-4 flex justify-center">{add}</div>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card px-5 py-2">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b">
                <th scope="col" className={th}>Name</th>
                <th scope="col" className={th}>Kind</th>
                <th scope="col" className={th}>Hint</th>
                <th scope="col" className={th}>Value set</th>
                <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.map((s) => (
                <tr key={s.id} className="border-b last:border-0">
                  <td className={td}>
                    <span className="font-medium">{s.name}</span>
                    {s.description && <div className="text-xs text-muted-foreground">{s.description}</div>}
                  </td>
                  <td className={td}>
                    <Badge>{kindText[s.kind]}</Badge>
                  </td>
                  <td className={td}>{s.hint ?? <span className="text-muted-foreground">–</span>}</td>
                  <td className={td}>{formatDateTime(s.valueUpdatedAt)}</td>
                  <td className={`${td} pr-0`}>
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" aria-label={`Replace the value of ${s.name}`} onClick={() => setReplacing(s)}>
                        <KeyRound aria-hidden /> Replace value
                      </Button>
                      <Button size="sm" variant="ghost" aria-label={`Rename ${s.name}`} onClick={() => setRenaming(s)}>
                        <Pencil aria-hidden /> Rename
                      </Button>
                      <Button size="sm" variant="destructive-ghost" aria-label={`Delete ${s.name}`} onClick={() => { del.reset(); setDeleting(s); }}>
                        <Trash2 aria-hidden /> Delete
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog open={creating} onClose={() => setCreating(false)} title="Add secret">
        {creating && <SecretForm onDone={() => setCreating(false)} onCancel={() => setCreating(false)} />}
      </Dialog>
      <Dialog open={!!replacing} onClose={() => setReplacing(null)} title={`Replace the value of “${replacing?.name ?? ""}”`}>
        {replacing && <SecretForm secret={replacing} onDone={() => setReplacing(null)} onCancel={() => setReplacing(null)} />}
      </Dialog>
      <Dialog open={!!renaming} onClose={() => setRenaming(null)} title="Rename secret">
        {renaming && <RenameForm key={renaming.etag} secret={renaming} onDone={() => setRenaming(null)} />}
      </Dialog>
      <Dialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title="Delete secret?"
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>Keep</Button>
            <Button variant="destructive" disabled={del.isPending} onClick={() => deleting?.id && del.mutate(deleting.id)}>
              {del.isPending ? "Deleting…" : "Delete secret"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          <span className="font-medium">{deleting?.name}</span> will be removed. Deployments already armed or running keep their own copy. A secret
          a profile, client or the backup settings use can’t be deleted.
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

/** Create a secret, or (with `secret`) replace its value. */
function SecretForm({ secret, onDone, onCancel }: { secret?: Secret; onDone: () => void; onCancel: () => void }) {
  const [kind, setKind] = useState<Kind>(secret?.kind ?? "password");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [value, setValue] = useState("");
  const [confirm, setConfirm] = useState("");
  const [username, setUsername] = useState(secret?.hint ?? "");
  const [file, setFile] = useState<File | null>(null);
  const isCred = credentialKinds.includes(kind);
  const isFile = fileKinds.includes(kind);
  const isPassword = kind === "password";
  const mismatch = isPassword && confirm !== value;
  const valid = !!(secret || name.trim()) && (isFile ? !!file : isCred ? !!username.trim() && !!value : !!value.trim() && !mismatch);

  const save = useMutation({
    mutationFn: async () => {
      let v: Value;
      if (isFile) v = { uploadId: await uploadSmall(file!) };
      else if (isCred) v = { credential: { username: username.trim(), password: value } };
      else v = { value };
      if (secret)
        return unwrap(
          await api.PUT("/secrets/{secretId}/value", { params: { path: { secretId: secret.id! }, header: { "If-Match": secret.etag! } }, body: v }),
        );
      return unwrap(await api.POST("/secrets", { body: { name: name.trim(), description: description.trim() || undefined, kind, ...v } }));
    },
    onSuccess: () => {
      refreshSecrets();
      onDone();
    },
  });
  const stale = save.error instanceof ApiError && save.error.status === 412;

  return (
    <FieldErrors value={save.error instanceof ApiError ? save.error.fields : []}>
      <form
        className="space-y-5"
        noValidate
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) save.mutate();
        }}
      >
        {!secret && (
          <>
            <Field label="Kind" hint={kindHelp[kind]}>
              {(p) => (
                <Select {...p} value={kind} onChange={(e) => { setKind(e.target.value as Kind); setValue(""); setConfirm(""); setFile(null); }}>
                  {creatable.map((k) => (
                    <option key={k} value={k}>
                      {kindText[k]}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label="Name" path="name" hint="How profiles and clients refer to it, e.g. “Lab admin password”.">
              {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            <Field label="Description" path="description">
              {(p) => <Input {...p} value={description} onChange={(e) => setDescription(e.target.value)} />}
            </Field>
          </>
        )}
        {secret && <p className="text-sm text-muted-foreground">{kindText[kind]}. The old value can’t be shown; enter the new one.</p>}
        {isCred && (
          <Field label="Username" path="credential.username" hint={kind === "domain-credential" ? "user@domain.example or DOMAIN\\user." : "DOMAIN\\user or user."}>
            {(p) => <Input {...p} autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />}
          </Field>
        )}
        {isFile ? (
          <Field label="File" path="uploadId" hint="At most 16 MiB. Uploaded and encrypted; the upload is deleted once it’s read.">
            {(p) => <Input {...p} type="file" className="py-1.5" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />}
          </Field>
        ) : (
          <>
            <Field label={kind === "product-key" ? "Product key" : "Password"} path={isCred ? "credential.password" : "value"}>
              {(p) => (
                <Input
                  {...p}
                  type={kind === "product-key" ? "text" : "password"}
                  autoComplete="new-password"
                  className={kind === "product-key" ? "font-mono uppercase" : undefined}
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                />
              )}
            </Field>
            {isPassword && (
              <Field label="Password again" hint={mismatch && confirm ? "The two don’t match." : undefined}>
                {(p) => <Input {...p} type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}
              </Field>
            )}
          </>
        )}
        {kind === "domain-credential" && (
          <p className="text-sm text-muted-foreground">
            A mistyped password only shows when a machine fails to join. To check it first, run{" "}
            <code className="font-mono">net use \\&lt;DC&gt;\IPC$ /user:{username || "user@domain"} *</code> on a machine that can reach the domain controller.
          </p>
        )}
        {save.isError && (
          <Alert tone="error" title={stale ? "Changed somewhere else" : undefined}>
            {stale ? "This secret was changed since you opened it. Close this and try again." : message(save.error)}
          </Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button onClick={onCancel}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!valid || save.isPending}>
            {save.isPending ? "Saving…" : secret ? "Replace value" : "Add secret"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}

function RenameForm({ secret, onDone }: { secret: Secret; onDone: () => void }) {
  const [name, setName] = useState(secret.name);
  const [description, setDescription] = useState(secret.description ?? "");
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/secrets/{secretId}", {
          params: { path: { secretId: secret.id! }, header: { "If-Match": secret.etag! } },
          body: { name: name.trim(), description: description.trim() },
        }),
      ),
    onSuccess: () => {
      refreshSecrets();
      onDone();
    },
  });
  return (
    <form
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) save.mutate();
      }}
    >
      <Field label="Name">{(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      <Field label="Description">{(p) => <Input {...p} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
      {save.isError && <Alert tone="error">{message(save.error)}</Alert>}
      <div className="flex justify-end gap-2">
        <Button onClick={onDone}>Cancel</Button>
        <Button type="submit" variant="primary" disabled={!name.trim() || save.isPending}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </div>
    </form>
  );
}
