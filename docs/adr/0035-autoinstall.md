# 35. Automated Linux installs: Ubuntu autoinstall templates and a per-deployment NoCloud seed

Status: accepted (2026-11-23). Phase 7 decision 4. Extends
[0027](0027-unattend-templates.md) (templates get a format) and
[0034](0034-linux-clients.md) (Linux profiles); builds on
[0008](0008-secrets-model.md) and the Phase 7 spike
([findings](../spikes/phase-7-findings.md)).

## Context
ADR 0034 boots Ubuntu's installer, which then asks everything at the
machine. The spike ran an autoinstall to the end from a NoCloud seed on our
content listener (`autoinstall ds=nocloud;s=<url>`, with
`cloud-config-url=/dev/null` next to casper's `url=`). The installer's
kernel takes its own DHCP address, so anything it fetches can't be tied to
the client by address, and its fetches are plain HTTP (no TLS client in the
initramfs; cloud-init wouldn't trust our CA). Windows already has templates
with masked previews, checks and snapshots (ADR 0027).

## Decision
- **Templates are shared.** `/unattend-templates` gets a `format`:
  `unattend` (as before, with `identityModes`) or `autoinstall` (an Ubuntu
  `user-data` document, no identity modes). The format can't change. A
  second built-in template, `builtin-autoinstall`, sits beside `builtin`.
  A profile's `unattendTemplateId` must name a template of its kind's
  format (`template_format_mismatch`); null is the kind's built-in one.
- **Autoinstall templates** are whole-document Go templates (ADR 0027's
  model) over `.Hostname`, `.Account` (`Username`, `RealName`,
  `PasswordHash`, `SSHAuthorizedKeys`, `SSHPasswordAuth`), `.Locale`,
  `.KeyboardLayout`, `.Timezone`, `.StorageLayout`, `.Packages`,
  `.BootOrderAfterDeploy`, `.Image`, `.Arch`, `.Client`, `.Profile`,
  `.Deployment`, `.Variables`. **Escaping:** every action's value is
  written as YAML (strings as JSON-quoted scalars, numbers and booleans
  bare, lists as flow sequences); `raw` opts out; `shquote` quotes for a
  shell. **Checks** (on save, with sample data with a password and with SSH
  keys only; when arming, with the real profile and client): `#cloud-config`
  first, valid YAML, an `autoinstall` mapping with `version: 1`, an
  `identity` (or `user-data`, or identity interactive); warnings when the
  hostname or username aren't the profile's.
- **Linux profiles** get `linux` (`LinuxSettings`): `install` `automatic`
  (default when present) or `interactive` (no seed; profiles without
  `linux` stay interactive); `account` (username, not a system account; a
  password secret and/or SSH keys, at least one); `hostnamePattern` (as
  computer name patterns, lowercased, a hostname label; default `linux-`
  and the MAC's last 6 hex digits); `storageLayout` `lvm`/`direct` on the
  largest disk; `locale`, `keyboardLayout`, `timezone`, `packages`.
  `bootOrderAfterDeploy` applies: the built-in template's `late-commands`
  put the installed `ubuntu` entry first for `disk-first` (curtin leaves
  the network first). Without a password the account is locked for password
  sign-in and gets passwordless sudo.
- **Arming** (or starting from the menu) an automatic install renders the
  hostname, snapshots the password secret, the template and the merged
  variables into the deployment (as Windows deployments do), after
  rendering it masked and refusing on error findings.
- **The seed**: when the boot commands are served, a deployment token is
  issued and the kernel command line gains
  `autoinstall ds=nocloud;s=http://<interface>:<httpPort>/content/seed/<token>/`.
  `/content/seed/<token>/` serves `user-data` (rendered on each request),
  `meta-data` (`instance-id` = the deployment id, `local-hostname`) and an
  empty `vendor-data`; `network-config` is 404 (cloud-init then keeps the
  installer's DHCP network; serving one would reconfigure the installer).
  The token works while the deployment runs; it's never logged.
- **Secrets**: `user-data` carries only a SHA-512 crypt hash of the
  password (`$6$`, 5000 rounds, a new 16-character salt on every request),
  never the password. Previews show `********`.

## Consequences
- The password hash crosses the deployment network in clear and could be
  attacked offline by someone who captures it; SSH keys avoid that. The
  seed URL is a bearer token in clear too, as the menu token is (ADR 0034).
- Anyone who learns a running deployment's seed URL can read its
  `user-data` (hash, keys, hostname) until the deployment ends.
- Templates of both formats share one list and one editor; the UI tells
  them apart. Copies of the built-in autoinstall template don't pick up
  later fixes to it.
- Debian preseed and RHEL kickstart would be further formats.
- To verify in the lab: the built-in template's install to the end on the
  NUC, the account (password and keys), the hostname, the boot order after
  the install, and the locked-password variant's sudo.
