import { useMutation, useQuery } from "@tanstack/react-query";
import { Copy, KeyRound, Plus, Trash2, UserPlus } from "lucide-react";
import { useState } from "react";
import { api, type Schemas } from "@/api/client";
import { queryClient } from "@/api/query";
import { Button } from "@/components/ui/button";
import { Field, FieldErrors, Input, Select } from "@/components/ui/form";
import { Alert, Badge, Dialog, PageHeader, Section, td, th } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { sessionChanged, useSession } from "@/lib/session";

type User = Schemas["User"];
type Token = Schemas["ApiToken"];

const MIN_PASSWORD = 8;

const codes: Record<string, string> = {
  username_taken: "Another account has this username (ignoring case).",
  last_user: "This is the only account. Create another before deleting it.",
  bad_credentials: "The current password is wrong.",
  too_many_attempts: "Too many wrong passwords.",
};
const message = (e: unknown) => (e instanceof ApiError && codes[e.code] ? `${codes[e.code]} ${e.code === "too_many_attempts" ? e.message : ""}`.trim() : errorMessage(e));

const refresh = () => {
  void queryClient.invalidateQueries({ queryKey: ["users"] });
  void queryClient.invalidateQueries({ queryKey: ["tokens"] });
};

/** New password twice; `valid` once they match and are long enough. */
function usePasswordPair() {
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const short = password.length > 0 && password.length < MIN_PASSWORD;
  const mismatch = confirm.length > 0 && confirm !== password;
  const fields = (path: string) => (
    <>
      <Field label="New password" path={path} hint={`At least ${MIN_PASSWORD} characters.`}>
        {(p) => <Input {...p} type="password" autoComplete="new-password" aria-invalid={short || undefined} value={password} onChange={(e) => setPassword(e.target.value)} />}
      </Field>
      <Field label="New password again">
        {(p) => <Input {...p} type="password" autoComplete="new-password" aria-invalid={mismatch || undefined} value={confirm} onChange={(e) => setConfirm(e.target.value)} />}
      </Field>
      {mismatch && <p className="-mt-2 text-sm text-destructive">The passwords don’t match.</p>}
    </>
  );
  return { password, valid: password.length >= MIN_PASSWORD && confirm === password, fields };
}

/**
 * Local admin accounts and API tokens (ADR 0029). Changes apply at once, so
 * this is its own page rather than part of the Settings form.
 */
export function AccountsPage() {
  const session = useSession();
  const me = session.data?.user;
  const users = useQuery({ queryKey: ["users"], queryFn: async () => unwrap(await api.GET("/users")).items });
  const tokens = useQuery({ queryKey: ["tokens"], queryFn: async () => unwrap(await api.GET("/tokens")).items });
  const [adding, setAdding] = useState(false);
  const [settingFor, setSettingFor] = useState<User | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const [creatingToken, setCreatingToken] = useState(false);
  const [revoking, setRevoking] = useState<Token | null>(null);

  const delUser = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/users/{userId}", { params: { path: { userId: id } } })),
    onSuccess: (_, id) => {
      setDeleting(null);
      if (id === me?.id) sessionChanged();
      else refresh();
    },
  });
  const revoke = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/tokens/{tokenId}", { params: { path: { tokenId: id } } })),
    onSuccess: () => {
      setRevoking(null);
      refresh();
    },
  });

  return (
    <div className="max-w-5xl space-y-6">
      <PageHeader
        title="Accounts"
        description="Everyone with an account has full admin rights. Scripts and automation use API tokens instead of an account’s password."
      />

      <OwnPassword />

      <Section
        title="Accounts"
        description="Deleting an account signs it out everywhere and revokes the API tokens it created."
        actions={
          <Button size="sm" onClick={() => setAdding(true)}>
            <UserPlus aria-hidden /> Add account
          </Button>
        }
      >
        {users.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : users.isError ? (
          <Alert tone="error">{errorMessage(users.error)}</Alert>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b">
                  <th scope="col" className={th}>Username</th>
                  <th scope="col" className={th}>Last signed in</th>
                  <th scope="col" className={th}>Password changed</th>
                  <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {users.data.map((u) => (
                  <tr key={u.id} className="border-b last:border-0">
                    <td className={td}>
                      <span className="font-medium">{u.username}</span> {u.id === me?.id && <Badge>You</Badge>}
                    </td>
                    <td className={td}>{u.lastSignInAt ? formatDateTime(u.lastSignInAt) : <span className="text-muted-foreground">Never</span>}</td>
                    <td className={td}>{formatDateTime(u.passwordChangedAt ?? u.createdAt)}</td>
                    <td className={`${td} pr-0`}>
                      <div className="flex justify-end gap-1">
                        {u.id !== me?.id && (
                          <Button size="sm" variant="ghost" aria-label={`Set the password of ${u.username}`} onClick={() => setSettingFor(u)}>
                            <KeyRound aria-hidden /> Set password
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="destructive-ghost"
                          aria-label={`Delete ${u.username}`}
                          disabled={users.data.length < 2}
                          title={users.data.length < 2 ? codes.last_user : undefined}
                          onClick={() => {
                            delUser.reset();
                            setDeleting(u);
                          }}
                        >
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
      </Section>

      <Section
        title="API tokens"
        description={
          <>
            For scripts and automation: send <code className="font-mono">Authorization: Bearer ndt_…</code>. A token can do everything an
            account can except manage accounts and tokens. Its secret is shown once.
          </>
        }
        actions={
          <Button size="sm" onClick={() => setCreatingToken(true)}>
            <Plus aria-hidden /> Create token
          </Button>
        }
      >
        {tokens.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : tokens.isError ? (
          <Alert tone="error">{errorMessage(tokens.error)}</Alert>
        ) : tokens.data.length === 0 ? (
          <p className="text-sm text-muted-foreground">No tokens.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b">
                  <th scope="col" className={th}>Name</th>
                  <th scope="col" className={th}>Created by</th>
                  <th scope="col" className={th}>Expires</th>
                  <th scope="col" className={th}>Last used</th>
                  <th scope="col" className={`${th} pr-0 text-right`}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {tokens.data.map((t) => {
                  const expired = !!t.expiresAt && new Date(t.expiresAt).getTime() <= Date.now();
                  return (
                    <tr key={t.id} className="border-b last:border-0">
                      <td className={td}>
                        <span className="font-medium">{t.name}</span>
                        <div className="text-xs text-muted-foreground">Created {formatDateTime(t.createdAt)}</div>
                      </td>
                      <td className={td}>{t.createdBy}</td>
                      <td className={td}>
                        {t.expiresAt ? formatDateTime(t.expiresAt) : "Never"} {expired && <Badge tone="error">Expired</Badge>}
                      </td>
                      <td className={td}>{t.lastUsedAt ? formatDateTime(t.lastUsedAt) : <span className="text-muted-foreground">Never</span>}</td>
                      <td className={`${td} pr-0`}>
                        <div className="flex justify-end">
                          <Button size="sm" variant="destructive-ghost" aria-label={`Revoke ${t.name}`} onClick={() => { revoke.reset(); setRevoking(t); }}>
                            <Trash2 aria-hidden /> Revoke
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
      </Section>

      <Dialog open={adding} onClose={() => setAdding(false)} title="Add account">
        {adding && <AddAccount onDone={() => setAdding(false)} />}
      </Dialog>
      <Dialog open={!!settingFor} onClose={() => setSettingFor(null)} title={`Set the password of ${settingFor?.username ?? ""}`}>
        {settingFor && <SetPassword user={settingFor} onDone={() => setSettingFor(null)} />}
      </Dialog>
      <Dialog open={creatingToken} onClose={() => setCreatingToken(false)} title="Create API token">
        {creatingToken && <CreateToken onDone={() => setCreatingToken(false)} />}
      </Dialog>
      <Dialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title="Delete account?"
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>Keep</Button>
            <Button variant="destructive" disabled={delUser.isPending} onClick={() => deleting && delUser.mutate(deleting.id)}>
              {delUser.isPending ? "Deleting…" : "Delete account"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          <span className="font-medium">{deleting?.username}</span> will be signed out everywhere, and the API tokens it created stop working.
          {deleting?.id === me?.id && " That includes you: you’ll be signed out now."}
        </p>
        {delUser.isError && (
          <Alert tone="error" className="mt-4">
            {message(delUser.error)}
          </Alert>
        )}
      </Dialog>
      <Dialog
        open={!!revoking}
        onClose={() => setRevoking(null)}
        title="Revoke token?"
        footer={
          <>
            <Button onClick={() => setRevoking(null)}>Keep</Button>
            <Button variant="destructive" disabled={revoke.isPending} onClick={() => revoking && revoke.mutate(revoking.id)}>
              {revoke.isPending ? "Revoking…" : "Revoke token"}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          Anything using <span className="font-medium">{revoking?.name}</span> gets “sign in required” from now on. This can’t be undone; create a
          new token instead.
        </p>
        {revoke.isError && (
          <Alert tone="error" className="mt-4">
            {message(revoke.error)}
          </Alert>
        )}
      </Dialog>
    </div>
  );
}

function OwnPassword() {
  const [current, setCurrent] = useState("");
  const [key, setKey] = useState(0);
  const pair = usePasswordPair();
  const change = useMutation({
    mutationFn: async () =>
      unwrap(await api.PUT("/auth/session/password", { body: { currentPassword: current, newPassword: pair.password } })),
    onSuccess: () => {
      setCurrent("");
      setKey((k) => k + 1);
      refresh();
    },
  });
  return (
    <Section title="Your password" description="Changing it signs you out everywhere else.">
      <FieldErrors value={change.error instanceof ApiError ? change.error.fields : []}>
        <form
          key={key}
          className="grid max-w-md gap-4"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (current && pair.valid) change.mutate();
          }}
        >
          <Field label="Current password" path="currentPassword">
            {(p) => <Input {...p} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}
          </Field>
          {pair.fields("newPassword")}
          {change.isError && <Alert tone="error">{message(change.error)}</Alert>}
          {change.isSuccess && <Alert tone="success">Password changed. Your other sessions have ended.</Alert>}
          <div>
            <Button type="submit" variant="primary" disabled={change.isPending || !current || !pair.valid}>
              {change.isPending ? "Changing…" : "Change password"}
            </Button>
          </div>
        </form>
      </FieldErrors>
    </Section>
  );
}

function AddAccount({ onDone }: { onDone: () => void }) {
  const [username, setUsername] = useState("");
  const pair = usePasswordPair();
  const add = useMutation({
    mutationFn: async () => unwrap(await api.POST("/users", { body: { username: username.trim(), password: pair.password } })),
    onSuccess: () => {
      refresh();
      onDone();
    },
  });
  return (
    <FieldErrors value={add.error instanceof ApiError ? add.error.fields : []}>
      <form
        className="space-y-4"
        noValidate
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          if (username.trim() && pair.valid) add.mutate();
        }}
      >
        <Field label="Username" path="username" hint="Letters, digits, dots, dashes and underscores. The account has full admin rights.">
          {(p) => <Input {...p} autoComplete="off" maxLength={64} value={username} onChange={(e) => setUsername(e.target.value)} />}
        </Field>
        {pair.fields("password")}
        {add.isError && <Alert tone="error">{message(add.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onDone}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={add.isPending || !username.trim() || !pair.valid}>
            {add.isPending ? "Adding…" : "Add account"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}

function SetPassword({ user, onDone }: { user: User; onDone: () => void }) {
  const pair = usePasswordPair();
  const set = useMutation({
    mutationFn: async () =>
      unwrap(await api.PUT("/users/{userId}/password", { params: { path: { userId: user.id } }, body: { password: pair.password } })),
    onSuccess: () => {
      refresh();
      onDone();
    },
  });
  return (
    <FieldErrors value={set.error instanceof ApiError ? set.error.fields : []}>
      <form
        className="space-y-4"
        noValidate
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          if (pair.valid) set.mutate();
        }}
      >
        <p className="text-sm text-muted-foreground">{user.username} is signed out everywhere. Their API tokens keep working.</p>
        {pair.fields("password")}
        {set.isError && <Alert tone="error">{message(set.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onDone}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={set.isPending || !pair.valid}>
            {set.isPending ? "Setting…" : "Set password"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}

const expiries = [
  { label: "Never", days: 0 },
  { label: "30 days", days: 30 },
  { label: "90 days", days: 90 },
  { label: "1 year", days: 365 },
];

function CreateToken({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [days, setDays] = useState(90);
  const [copied, setCopied] = useState(false);
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/tokens", {
          body: { name: name.trim(), expiresAt: days ? new Date(Date.now() + days * 86_400_000).toISOString() : null },
        }),
      ),
    onSuccess: refresh,
  });

  if (create.data)
    return (
      <div className="space-y-4">
        <Alert tone="warning" title="Copy the token now">
          This is the only time it is shown. Store it where your script reads it, not in source control.
        </Alert>
        <div className="flex gap-2">
          <Input readOnly aria-label="Token" className="font-mono text-xs" value={create.data.secret} onFocus={(e) => e.currentTarget.select()} />
          <Button
            aria-label="Copy the token"
            onClick={() => {
              void navigator.clipboard?.writeText(create.data.secret).then(() => setCopied(true));
            }}
          >
            <Copy aria-hidden /> {copied ? "Copied" : "Copy"}
          </Button>
        </div>
        <div className="flex justify-end">
          <Button variant="primary" onClick={onDone}>
            Done
          </Button>
        </div>
      </div>
    );

  return (
    <FieldErrors value={create.error instanceof ApiError ? create.error.fields : []}>
      <form
        className="space-y-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) create.mutate();
        }}
      >
        <Field label="Name" path="name" hint="What uses it, e.g. “nightly build pipeline”.">
          {(p) => <Input {...p} maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Expires" path="expiresAt">
          {(p) => (
            <Select {...p} value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {expiries.map((x) => (
                <option key={x.days} value={x.days}>
                  {x.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {create.isError && <Alert tone="error">{message(create.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onDone}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={create.isPending || !name.trim()}>
            {create.isPending ? "Creating…" : "Create token"}
          </Button>
        </div>
      </form>
    </FieldErrors>
  );
}
