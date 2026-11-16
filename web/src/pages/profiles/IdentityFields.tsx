import { Plus, Trash2 } from "lucide-react";
import type { Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input, NumberInput, RadioCards, Select, Textarea } from "@/components/ui/form";
import { Alert } from "@/components/ui/misc";
import { useSecrets } from "@/pages/clients/data";

export type Identity = Schemas["Identity"];
export type OsSettings = Schemas["OsSettings"];
/** osSettings while editing: empty fields take the server's defaults. */
export type OsDraft = Partial<OsSettings>;

/** The draft with the spec's defaults filled in, as ProfileInput wants it. */
export function osToInput(d: OsDraft): OsSettings {
  return {
    computerNamePattern: d.computerNamePattern?.trim() || null,
    uiLanguage: d.uiLanguage?.trim() || "en-GB",
    systemLocale: d.systemLocale?.trim() || "en-GB",
    userLocale: d.userLocale?.trim() || "en-GB",
    inputLocale: d.inputLocale?.trim() || "0809:00000809",
    timeZone: d.timeZone?.trim() || "GMT Standard Time",
    productKeySecretId: d.productKeySecretId || null,
  };
}
type Account = Schemas["IdentityLocal"]["accounts"][number];

/** Built-in Windows accounts Setup won't create (validate refuses them too). */
const reserved = ["administrator", "guest", "defaultaccount", "wdagutilityaccount"];

/** Problems that stop saving, by field; empty when the identity is complete. */
export function identityProblems(id: Identity): string[] {
  const out: string[] = [];
  const checkName = (n: string | undefined, what: string) => {
    if (!n?.trim()) out.push(`${what} needs a name.`);
    else if (n.length > 20) out.push(`${what}: names are at most 20 characters.`);
    else if (reserved.includes(n.trim().toLowerCase())) out.push(`${what}: “${n}” is a built-in Windows account; choose another name.`);
  };
  if (id.mode === "local") {
    if (id.accounts.length === 0) out.push("Add at least one account.");
    id.accounts.forEach((a, i) => {
      checkName(a.name, `Account ${i + 1}`);
      if (!a.passwordSecretId) out.push(`Account ${i + 1} needs a password.`);
    });
  }
  if (id.mode === "ad") {
    if (id.joinMethod === "credentials") {
      if (!id.domain?.trim()) out.push("Enter the domain to join.");
      if (!id.joinCredentialSecretId) out.push("Choose the join account.");
    }
    if (id.localAdmin) {
      checkName(id.localAdmin.name, "The local administrator");
      if (!id.localAdmin.passwordSecretId) out.push("The local administrator needs a password.");
    }
  }
  return out;
}

function SecretSelect({ kind, value, onChange, label, hint }: { kind: Schemas["SecretKind"]; value: string; onChange: (id: string) => void; label: string; hint?: string }) {
  const secrets = useSecrets(kind);
  const items = secrets.data?.items ?? [];
  return (
    <Field label={label} hint={items.length === 0 && !secrets.isPending ? "None yet: add one on the Secrets page." : hint}>
      {(p) => (
        <Select {...p} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Choose…</option>
          {items.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {s.hint ? ` (${s.hint})` : ""}
            </option>
          ))}
        </Select>
      )}
    </Field>
  );
}

export function IdentityFields({ value, onChange }: { value: Identity; onChange: (v: Identity) => void }) {
  const setMode = (mode: Identity["mode"]) => {
    if (mode === value.mode) return;
    if (mode === "none") onChange({ mode: "none", skipEula: true });
    if (mode === "local") onChange({ mode: "local", accounts: [{ name: "", group: "Administrators", passwordSecretId: "" }], hideOobe: true });
    if (mode === "ad") onChange({ mode: "ad", joinMethod: "odj" });
  };
  return (
    <div className="space-y-5">
      <RadioCards
        legend="Accounts"
        value={value.mode === "entra" ? "none" : value.mode}
        onChange={setMode}
        options={[
          { value: "none", label: "None", description: "Whoever first starts the machine goes through Windows setup." },
          { value: "local", label: "Local accounts", description: "Created during Setup; the setup screens can be skipped." },
          { value: "ad", label: "Active Directory", description: "Joined to a domain during Setup, online or offline." },
        ]}
      />
      {value.mode === "entra" && <Alert tone="warning">Microsoft Entra profiles arrive in a later phase and can’t be edited here.</Alert>}
      {value.mode === "none" && (
        <Checkbox label="Skip the licence page" checked={value.skipEula} onChange={(skipEula) => onChange({ ...value, skipEula })} />
      )}
      {value.mode === "local" && <LocalFields value={value} onChange={onChange} />}
      {value.mode === "ad" && <AdFields value={value} onChange={onChange} />}
    </div>
  );
}

function LocalFields({ value, onChange }: { value: Schemas["IdentityLocal"]; onChange: (v: Identity) => void }) {
  const setAccount = (i: number, a: Partial<Account>) =>
    onChange({ ...value, accounts: value.accounts.map((x, j) => (j === i ? { ...x, ...a } : x)) });
  const names = value.accounts.map((a) => a.name).filter(Boolean);
  return (
    <div className="space-y-4">
      {value.accounts.map((a, i) => (
        <fieldset key={i} className="rounded-md border p-4">
          <legend className="px-1 text-sm font-medium">Account {i + 1}</legend>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" hint="Up to 20 characters; not a built-in name such as Administrator.">
              {(p) => <Input {...p} maxLength={20} value={a.name} onChange={(e) => setAccount(i, { name: e.target.value })} />}
            </Field>
            <Field label="Display name">
              {(p) => <Input {...p} value={a.displayName ?? ""} onChange={(e) => setAccount(i, { displayName: e.target.value || undefined })} />}
            </Field>
            <Field label="Group">
              {(p) => (
                <Select {...p} value={a.group} onChange={(e) => setAccount(i, { group: e.target.value as Account["group"] })}>
                  <option value="Administrators">Administrators</option>
                  <option value="Users">Users</option>
                </Select>
              )}
            </Field>
            <SecretSelect kind="password" label="Password" value={a.passwordSecretId} onChange={(id) => setAccount(i, { passwordSecretId: id })} />
          </div>
          {value.accounts.length > 1 && (
            <div className="mt-3 flex justify-end">
              <Button size="sm" variant="destructive-ghost" onClick={() => onChange({ ...value, accounts: value.accounts.filter((_, j) => j !== i) })}>
                <Trash2 aria-hidden /> Remove account
              </Button>
            </div>
          )}
        </fieldset>
      ))}
      <Button size="sm" onClick={() => onChange({ ...value, accounts: [...value.accounts, { name: "", group: "Users", passwordSecretId: "" }] })}>
        <Plus aria-hidden /> Add account
      </Button>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Sign in automatically as" hint="After Setup, so the machine is ready to use.">
          {(p) => (
            <Select
              {...p}
              value={value.autoLogon?.account ?? ""}
              onChange={(e) => onChange({ ...value, autoLogon: e.target.value ? { account: e.target.value, count: value.autoLogon?.count ?? 1 } : null })}
            >
              <option value="">Nobody</option>
              {names.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {value.autoLogon && (
          <Field label="Times" hint="Automatic sign-ins before it asks for the password (1–10).">
            {(p) => (
              <NumberInput
                {...p}
                min={1}
                max={10}
                value={value.autoLogon?.count}
                onChange={(n) => onChange({ ...value, autoLogon: { ...value.autoLogon!, count: n ?? 1 } })}
              />
            )}
          </Field>
        )}
      </div>
      <Checkbox
        label="Skip the setup screens"
        hint="No account, online-account, privacy or Wi-Fi questions when Windows first starts."
        checked={value.hideOobe}
        onChange={(hideOobe) => onChange({ ...value, hideOobe })}
      />
    </div>
  );
}

function AdFields({ value, onChange }: { value: Schemas["IdentityAd"]; onChange: (v: Identity) => void }) {
  return (
    <div className="space-y-4">
      <RadioCards
        legend="Join"
        value={value.joinMethod}
        onChange={(joinMethod) => onChange({ ...value, joinMethod })}
        options={[
          {
            value: "odj",
            label: "Offline (recommended)",
            description: "Each client gets its own blob from djoin /provision. No password reaches the machine and no domain controller is needed during Setup.",
          },
          {
            value: "credentials",
            label: "With an account",
            description: "Setup joins online: the machine must reach a domain controller, and DHCP must hand out its DNS server.",
          },
        ]}
      />
      {value.joinMethod === "odj" ? (
        <p className="text-sm text-muted-foreground">
          Add each machine’s blob on the Secrets page and choose it on the client. The blob names the computer, so the computer name pattern below is ignored.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Domain" hint="The DNS name, e.g. corp.example.">
            {(p) => <Input {...p} value={value.domain ?? ""} onChange={(e) => onChange({ ...value, domain: e.target.value })} />}
          </Field>
          <SecretSelect
            kind="domain-credential"
            label="Join account"
            value={value.joinCredentialSecretId ?? ""}
            onChange={(id) => onChange({ ...value, joinCredentialSecretId: id || null })}
          />
          <Field label="Organisational unit" className="sm:col-span-2" hint="Optional, e.g. OU=Lab,DC=corp,DC=example. Empty uses the domain’s default.">
            {(p) => (
              <Input {...p} value={value.organizationalUnit ?? ""} onChange={(e) => onChange({ ...value, organizationalUnit: e.target.value || null })} />
            )}
          </Field>
        </div>
      )}
      <Checkbox
        label="Also create a local administrator"
        hint="A way in if the join fails, or the domain can’t be reached."
        checked={!!value.localAdmin}
        onChange={(on) => onChange({ ...value, localAdmin: on ? { name: "breakglass", passwordSecretId: "" } : null })}
      />
      {value.localAdmin && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" hint="Not a built-in name such as Administrator.">
            {(p) => (
              <Input {...p} maxLength={20} value={value.localAdmin?.name ?? ""} onChange={(e) => onChange({ ...value, localAdmin: { ...value.localAdmin, name: e.target.value } })} />
            )}
          </Field>
          <SecretSelect
            kind="password"
            label="Password"
            value={value.localAdmin.passwordSecretId ?? ""}
            onChange={(id) => onChange({ ...value, localAdmin: { ...value.localAdmin, passwordSecretId: id } })}
          />
        </div>
      )}
    </div>
  );
}

/** The parts of osSettings the unattend uses. Empty fields fall back to the server's defaults. */
export function OsFields({ value, onChange }: { value: OsDraft; onChange: (v: OsDraft) => void }) {
  const set = (k: keyof OsDraft) => (e: { target: { value: string } }) => onChange({ ...value, [k]: e.target.value || undefined });
  return (
    <div className="space-y-4">
      <Field
        label="Computer name"
        hint={
          <>
            A pattern, e.g. <code className="font-mono">LAB-{"{{ .Client.Serial | last 4 }}"}</code>: <code className="font-mono">.Client.Serial</code>,{" "}
            <code className="font-mono">.MAC</code>, <code className="font-mono">.Hostname</code>, <code className="font-mono">.Variables.name</code>; functions{" "}
            <code className="font-mono">first n</code>, <code className="font-mono">last n</code>, <code className="font-mono">upper</code>,{" "}
            <code className="font-mono">lower</code>. Up to 15 letters, digits and hyphens. Empty lets Windows choose.
          </>
        }
      >
        {(p) => (
          <Input
            {...p}
            className="font-mono"
            value={value.computerNamePattern ?? ""}
            onChange={(e) => onChange({ ...value, computerNamePattern: e.target.value || null })}
          />
        )}
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Time zone" hint="A Windows time zone ID. Default GMT Standard Time.">
          {(p) => <Input {...p} value={value.timeZone ?? ""} placeholder="GMT Standard Time" onChange={set("timeZone")} />}
        </Field>
        <Field label="Keyboard" hint="Input locale. Default 0809:00000809 (UK).">
          {(p) => <Input {...p} className="font-mono" value={value.inputLocale ?? ""} placeholder="0809:00000809" onChange={set("inputLocale")} />}
        </Field>
        <Field label="Display language" hint="Must be in the image. Default en-GB.">
          {(p) => <Input {...p} value={value.uiLanguage ?? ""} placeholder="en-GB" onChange={set("uiLanguage")} />}
        </Field>
        <Field label="Region formats" hint="User locale. Default en-GB.">
          {(p) => <Input {...p} value={value.userLocale ?? ""} placeholder="en-GB" onChange={set("userLocale")} />}
        </Field>
        <Field label="System locale" hint="For non-Unicode programs. Default en-GB.">
          {(p) => <Input {...p} value={value.systemLocale ?? ""} placeholder="en-GB" onChange={set("systemLocale")} />}
        </Field>
        <SecretSelect
          kind="product-key"
          label="Product key"
          hint="Optional. Empty uses the edition’s default or KMS."
          value={value.productKeySecretId ?? ""}
          onChange={(id) => onChange({ ...value, productKeySecretId: id || null })}
        />
      </div>
    </div>
  );
}

/** "name=value" lines ↔ variables. */
export function parseVariables(text: string): { vars: Record<string, string>; bad: string[] } {
  const vars: Record<string, string> = {};
  const bad: string[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const i = line.indexOf("=");
    if (i <= 0) bad.push(line);
    else vars[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return { vars, bad };
}

export function VariablesField({ text, onChange, hint }: { text: string; onChange: (t: string) => void; hint: string }) {
  const { bad } = parseVariables(text);
  return (
    <Field label="Variables" hint={bad.length ? `Each line needs name=value: ${bad.join(", ")}` : hint}>
      {(p) => <Textarea {...p} className="font-mono" rows={3} value={text} onChange={(e) => onChange(e.target.value)} placeholder="site=ldn" />}
    </Field>
  );
}
