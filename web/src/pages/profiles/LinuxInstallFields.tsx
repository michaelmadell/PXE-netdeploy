import { Link } from "react-router";
import type { Schemas } from "@/api/client";
import { Checkbox, Field, Input, RadioCards, Select, Textarea } from "@/components/ui/form";
import { BUILTIN_AUTOINSTALL, formatOf, useTemplates } from "@/pages/templates/data";
import { SecretSelect } from "./IdentityFields";

type Profile = Schemas["Profile"];
type Settings = Schemas["LinuxSettings"];

/** The Linux install settings as the form edits them (ADR 0035). */
export type LinuxDraft = {
  install: "automatic" | "interactive";
  username: string;
  realName: string;
  passwordSecretId: string;
  keys: string;
  sshPasswordAuth: boolean;
  hostnamePattern: string;
  storageLayout: "lvm" | "direct";
  locale: string;
  keyboardLayout: string;
  timezone: string;
  packages: string;
  templateId: string;
  bootOrder: "disk-first" | "network-first";
};

export function linuxDraft(p: Profile | null): LinuxDraft {
  const l = p?.kind === "linux" ? p.linux : undefined;
  return {
    // Profiles from before automatic installs have no settings: interactive.
    install: p?.kind === "linux" && !l ? "interactive" : (l?.install ?? "automatic"),
    username: l?.account?.username ?? "",
    realName: l?.account?.realName ?? "",
    passwordSecretId: l?.account?.passwordSecretId ?? "",
    keys: (l?.account?.sshAuthorizedKeys ?? []).join("\n"),
    sshPasswordAuth: l?.account?.sshPasswordAuth ?? false,
    hostnamePattern: l?.hostnamePattern ?? "",
    storageLayout: l?.storageLayout ?? "lvm",
    locale: l?.locale ?? "en_GB.UTF-8",
    keyboardLayout: l?.keyboardLayout ?? "gb",
    timezone: l?.timezone ?? "Europe/London",
    packages: (l?.packages ?? []).join(" "),
    templateId: (p?.kind === "linux" && p.unattendTemplateId) || BUILTIN_AUTOINSTALL,
    bootOrder: p?.kind === "linux" ? (p.bootOrderAfterDeploy ?? "disk-first") : "disk-first",
  };
}

const keyList = (s: string) => s.split("\n").map((k) => k.trim()).filter(Boolean);
const packageList = (s: string) => [...new Set(s.split(/[\s,]+/).filter(Boolean))];

/** What's wrong with the draft, in words; empty when it can be saved. */
export function linuxProblems(d: LinuxDraft): string[] {
  if (d.install === "interactive") return [];
  const out: string[] = [];
  if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(d.username)) out.push("The username needs lower-case letters, digits, - or _, starting with a letter.");
  if (!d.passwordSecretId && keyList(d.keys).length === 0) out.push("The account needs a password or an SSH key.");
  if (keyList(d.keys).some((k) => k.split(/\s+/).length < 2)) out.push("Each SSH key is one line: its type, the key, and an optional comment.");
  if (packageList(d.packages).some((p) => !/^[a-z0-9][a-z0-9+.-]*$/.test(p))) out.push("Package names are lower-case letters, digits, +, - and dots.");
  return out;
}

/** The profile fields an automatic Linux install adds. */
export function linuxInput(d: LinuxDraft): Pick<Schemas["ProfileInput"], "linux" | "unattendTemplateId" | "bootOrderAfterDeploy"> {
  const settings: Settings = {
    install: d.install,
    hostnamePattern: d.hostnamePattern.trim() || null,
    storageLayout: d.storageLayout,
    locale: d.locale.trim() || "en_GB.UTF-8",
    keyboardLayout: d.keyboardLayout.trim() || "gb",
    timezone: d.timezone.trim() || "Europe/London",
    packages: packageList(d.packages),
  };
  if (d.username.trim())
    settings.account = {
      username: d.username.trim(),
      realName: d.realName.trim() || undefined,
      passwordSecretId: d.passwordSecretId || null,
      sshAuthorizedKeys: keyList(d.keys),
      sshPasswordAuth: d.sshPasswordAuth,
    };
  return { linux: settings, unattendTemplateId: d.templateId === BUILTIN_AUTOINSTALL ? null : d.templateId, bootOrderAfterDeploy: d.bootOrder };
}

export function LinuxInstallFields({ value, onChange }: { value: LinuxDraft; onChange: (d: LinuxDraft) => void }) {
  const templates = useTemplates();
  const set = (p: Partial<LinuxDraft>) => onChange({ ...value, ...p });
  const autoTemplates = (templates.data ?? []).filter((t) => formatOf(t) === "autoinstall");
  return (
    <section className="space-y-5 border-t pt-5">
      <RadioCards
        legend="Install"
        value={value.install}
        onChange={(install) => set({ install })}
        options={[
          { value: "automatic", label: "Automatic", description: "Ubuntu’s autoinstall: the machine is erased and installed with the settings below, with no questions." },
          { value: "interactive", label: "Interactive", description: "The installer asks everything at the machine, as when booting the ISO." },
        ]}
      />
      {value.install === "automatic" && (
        <>
          <h3 className="text-sm font-semibold">Account</h3>
          <div className="grid gap-5 sm:grid-cols-2">
            <Field label="Username" path="linux.account.username">
              {(p) => <Input {...p} autoComplete="off" maxLength={32} value={value.username} onChange={(e) => set({ username: e.target.value })} />}
            </Field>
            <Field label="Full name" path="linux.account.realName">
              {(p) => <Input {...p} maxLength={200} value={value.realName} onChange={(e) => set({ realName: e.target.value })} />}
            </Field>
          </div>
          <SecretSelect
            kind="password"
            label="Password"
            hint="Optional with SSH keys. The installer gets only a hash of it, over plain HTTP. Without one, the account signs in with keys and sudo doesn’t ask."
            value={value.passwordSecretId}
            onChange={(passwordSecretId) => set({ passwordSecretId })}
          />
          <Field label="SSH public keys" path="linux.account.sshAuthorizedKeys" hint="One per line, as in authorized_keys. Any key installs the OpenSSH server.">
            {(p) => (
              <Textarea {...p} className="font-mono" rows={3} value={value.keys} onChange={(e) => set({ keys: e.target.value })} placeholder="ssh-ed25519 AAAA… you@laptop" />
            )}
          </Field>
          <Checkbox label="Allow SSH sign-in with the password" checked={value.sshPasswordAuth} onChange={(sshPasswordAuth) => set({ sshPasswordAuth })} />

          <h3 className="border-t pt-5 text-sm font-semibold">System</h3>
          <Field
            label="Hostname pattern"
            path="linux.hostnamePattern"
            hint={<>As computer name patterns, e.g. <code className="font-mono">{"lab-{{ .Client.Serial | last 6 }}"}</code>; lower-cased. Empty: linux- and the end of the MAC.</>}
          >
            {(p) => <Input {...p} className="font-mono" value={value.hostnamePattern} onChange={(e) => set({ hostnamePattern: e.target.value })} />}
          </Field>
          <div className="grid gap-5 sm:grid-cols-3">
            <Field label="Locale" path="linux.locale">
              {(p) => <Input {...p} value={value.locale} onChange={(e) => set({ locale: e.target.value })} />}
            </Field>
            <Field label="Keyboard layout" path="linux.keyboardLayout">
              {(p) => <Input {...p} value={value.keyboardLayout} onChange={(e) => set({ keyboardLayout: e.target.value })} />}
            </Field>
            <Field label="Time zone" path="linux.timezone">
              {(p) => <Input {...p} value={value.timezone} onChange={(e) => set({ timezone: e.target.value })} />}
            </Field>
          </div>
          <RadioCards
            legend="Disk layout (the largest disk is erased)"
            value={value.storageLayout}
            onChange={(storageLayout) => set({ storageLayout })}
            options={[
              { value: "lvm", label: "LVM", description: "Ubuntu’s default: the root file system in a logical volume." },
              { value: "direct", label: "Plain partitions", description: "Partitions only, no LVM." },
            ]}
          />
          <Field label="Extra packages" path="linux.packages" hint="Separated by spaces or commas.">
            {(p) => <Input {...p} className="font-mono" value={value.packages} onChange={(e) => set({ packages: e.target.value })} placeholder="htop curl" />}
          </Field>
          <RadioCards
            legend="After installing, the machine starts from"
            value={value.bootOrder}
            onChange={(bootOrder) => set({ bootOrder })}
            options={[
              { value: "disk-first", label: "Its disk", description: "The built-in template puts Ubuntu first in the firmware’s boot order." },
              { value: "network-first", label: "The network", description: "Every start shows netdeploy’s countdown first, then continues to the disk." },
            ]}
          />
          <Field
            label="Autoinstall template"
            path="unattendTemplateId"
            hint={
              <>
                The user-data the installer reads. <Link to="/templates" className="text-primary underline-offset-4 hover:underline">Manage templates</Link>
              </>
            }
          >
            {(p) => (
              <Select {...p} value={value.templateId} onChange={(e) => set({ templateId: e.target.value })}>
                {!templates.data && <option value={BUILTIN_AUTOINSTALL}>Built-in</option>}
                {autoTemplates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </>
      )}
    </section>
  );
}
