import type { ReactNode } from "react";

const row = (name: ReactNode, what: ReactNode) => (
  <tr className="border-b last:border-0">
    <td className="py-1.5 pr-4 align-top font-mono text-xs whitespace-nowrap">{name}</td>
    <td className="py-1.5 align-top">{what}</td>
  </tr>
);

/** What a template can use (the spec's UnattendTemplateInput.content, ADR 0027). */
export function Reference() {
  return (
    <details className="rounded-md border bg-muted/40 px-4 py-3 text-sm">
      <summary className="cursor-pointer font-medium">What a template can use</summary>
      <div className="mt-3 space-y-4">
        <p className="text-muted-foreground">
          Go template syntax: <code className="font-mono">{"{{ .Field }}"}</code>, <code className="font-mono">{"{{ if … }}…{{ end }}"}</code>,{" "}
          <code className="font-mono">{"{{ with … }}"}</code>, <code className="font-mono">{"{{ range … }}"}</code>. A field that doesn’t exist, or a variable that isn’t
          a default or required, stops the template from saving. Secret values (†) are shown as <code className="font-mono">********</code> in previews and are already in
          the form the answer file wants.
        </p>
        <table className="w-full">
          <tbody>
            {row(".Arch", "amd64 or arm64, from the Windows image. Use it for every component’s processorArchitecture.")}
            {row(".ComputerName", "From the profile’s name pattern. Empty when Windows picks the name, or the offline join blob names the computer.")}
            {row(".OS.TimeZone, .OS.UILanguage, .OS.SystemLocale, .OS.UserLocale, .OS.InputLocale", "The profile’s Windows settings.")}
            {row(".OS.ProductKey †", "Empty when the profile has none.")}
            {row(".Identity.Mode", "none, local or ad.")}
            {row(".Identity.HideEULA, .Identity.HideOOBE", "Whether to skip the licence page and the setup screens.")}
            {row(".Identity.Accounts", <>Local accounts, or Active Directory’s local administrator: <code className="font-mono">.Name .DisplayName .Group .Password†</code></>)}
            {row(".Identity.AutoLogon", <>Or nothing: <code className="font-mono">.Username .Password† .Count</code></>)}
            {row(".Identity.Join", <>Active Directory with a join account, or nothing: <code className="font-mono">.Domain .Username† .Password† .JoinDomain .OU</code></>)}
            {row(".Identity.ODJ", <>Active Directory offline join, or nothing: <code className="font-mono">.AccountData†</code></>)}
            {row(".Client.Serial, .Client.MAC, .Client.SMBIOSUUID, .Client.Hostname, .Client.ID", "The machine.")}
            {row(".Profile.Name, .Profile.ID, .Deployment.ID", <>The deployment’s id is <code className="font-mono">preview</code> in previews.</>)}
            {row(".Variables.name", "Template defaults, then the profile’s, then the client’s.")}
            {row(".InstallMethod", "agent. The windowsPE pass is left out of what the agent gets.")}
          </tbody>
        </table>
        <table className="w-full">
          <tbody>
            {row("first n, last n", <>Characters from a value: <code className="font-mono">{"{{ .Client.Serial | last 4 }}"}</code></>)}
            {row("upper, lower", "Change case.")}
            {row('default "x" value', <>A fallback for an empty value: <code className="font-mono">{'{{ default "ldn" (index .Variables "site") }}'}</code></>)}
            {row("raw", "Write a value without escaping it, for XML held in a variable.")}
          </tbody>
        </table>
      </div>
    </details>
  );
}

/** What an autoinstall template can use (the spec's UnattendTemplateInput.content, ADR 0035). */
export function AutoinstallReference() {
  return (
    <details className="rounded-md border bg-muted/40 px-4 py-3 text-sm">
      <summary className="cursor-pointer font-medium">What a template can use</summary>
      <div className="mt-3 space-y-4">
        <p className="text-muted-foreground">
          Go template syntax, as for unattend templates. The output is Ubuntu’s autoinstall <code className="font-mono">user-data</code>: it must start with{" "}
          <code className="font-mono">#cloud-config</code> and have an <code className="font-mono">autoinstall:</code> section with{" "}
          <code className="font-mono">version: 1</code> and an <code className="font-mono">identity</code>. The password hash (†) is shown as{" "}
          <code className="font-mono">********</code> in previews.
        </p>
        <table className="w-full">
          <tbody>
            {row(".Hostname", "From the profile’s hostname pattern, lowercased.")}
            {row(".Account.Username, .Account.RealName", "The account the installer creates.")}
            {row(".Account.PasswordHash †", "A SHA-512 crypt hash ($6$…), made with a new salt each time the installer asks. Empty without a password.")}
            {row(".Account.SSHAuthorizedKeys, .Account.SSHPasswordAuth", "A list of OpenSSH public keys, and whether SSH accepts the password.")}
            {row(".Locale, .KeyboardLayout, .Timezone", "For example en_GB.UTF-8, gb, Europe/London.")}
            {row(".StorageLayout", "lvm or direct, on the largest disk.")}
            {row(".Packages", "A list of extra packages.")}
            {row(".BootOrderAfterDeploy", "disk-first or network-first.")}
            {row(".Arch, .Image.Distribution, .Image.Version", "amd64 or arm64, and the image’s distribution and version.")}
            {row(".Client.*, .Profile.*, .Deployment.ID, .Variables.name", "As for unattend templates.")}
          </tbody>
        </table>
        <table className="w-full">
          <tbody>
            {row("first n, last n, upper, lower, default", "As for unattend templates.")}
            {row("raw", "Write a value as it is, not as a quoted YAML value.")}
            {row("shquote", <>Quote text for a shell command: <code className="font-mono">{"{{ raw (shquote .Hostname) }}"}</code> in a block scalar.</>)}
          </tbody>
        </table>
      </div>
    </details>
  );
}
