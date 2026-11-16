# 27. Unattend templates

Status: accepted (2026-11-16). Amends [0007](0007-agent-and-smb-install-methods.md)
and the built-in unattend in [0026](0026-deployments.md); builds on
[0008](0008-secrets-model.md).

## Context
ADR 0026 renders one built-in unattend per deployment, in code, for the
`none`, `local` and `ad` identity modes. Admins need their own settings
(first-logon commands, extra components, existing WSIM/MDT files) without
losing what Phase 4 established: the ODJ blob travels in the unattend
(WinPE has no `djoin.exe`), Setup won't create accounts with built-in names,
and the rendered file is never stored. Secrets must not be readable in a
preview.

## Decision
**Language: a Go `text/template` that writes the whole document**, not XML
fragments merged into the built-in one. Admins can paste an existing file
and add placeholders; there are no merge rules to learn. The cost: a copy of
the built-in template doesn't pick up later fixes to it (no shared partials,
which would be a compatibility surface).

**Data**: the same values the built-in uses, built by Go code per identity
mode, so the built-in is just another template. `.Arch`, `.InstallMethod`,
`.ComputerName`, `.OS` (locales, time zone, `ProductKey`), `.Identity`
(`Mode`, `HideEULA`, `HideOOBE`, `Accounts`, `AutoLogon`, `Join`, `ODJ`),
`.Client`, `.Profile`, `.Deployment`, `.Variables` (template defaults <
profile < client; the name `.Variables` matches computer name patterns).
Secret fields are already in unattend form (encoded passwords, ODJ
`AccountData`). Functions: `first`, `last`, `upper`, `lower`, `default`,
`raw`. Parsed with `missingkey=error`, so typos and undeclared variables fail
when saving.

**Escaping**: when parsing, every action that writes output gets an
XML-escape step appended (as `html/template` does); only a value passed
through `raw` is written unescaped.

**Secrets in previews**: a preview never decrypts anything. Secret fields
hold `********`, so no template can print a value. Only `GET /agent/unattend`
decrypts (ADR 0008). Variables are plain text and must not hold secrets.
Secrets a template declares itself (named slots a profile maps to secrets)
are deferred; they would be additive.

**Checks.** On save, the template renders with sample data for each of its
`identityModes` (`ad` with both join methods), using marker values:
- errors: not well-formed XML; root not `unattend` in
  `urn:schemas-microsoft-com:unattend`; unknown or repeated pass; an XML
  declaration other than UTF-8; `local` without every account under
  `oobeSystem/…Shell-Setup/UserAccounts/LocalAccounts` or without the
  auto-logon; `ad` credentials without `specialize/…UnattendedJoin/
  Identification` `JoinDomain` and `Credentials`; `ad` ODJ without
  `Provisioning/AccountData`; a `LocalAccount/Name` that is a built-in name
  (`reserved_account_name`, also for names typed into the template);
- warnings: `.ComputerName` dropped though a pattern is set; a
  `ComputerName` rendered under ODJ.

Profile validation and arming render with the real data (secrets as
`********`) and add: a `processorArchitecture` other than `.Arch` (error);
a `requiredVariables` entry with no value (error when arming, a warning per
assigned client in `validate`); the profile's mode not in `identityModes`.
In agent mode the `windowsPE` pass is removed after rendering (Setup would
ignore it on an applied image; this keeps its contents off the disk). One
template still renders for both install methods (ADR 0007).

**Built-in template**: one, in code, id `builtin`, `builtIn: true`, all
supported modes; listed first, readable, previewable, copyable; `PUT` and
`DELETE` are `409 built_in`. A profile's `unattendTemplateId: null` (or
`builtin`, stored as null) uses it. Its output equals the Phase 4 unattend.

**Storage and snapshots**: table `unattend_templates`; profiles reference it
with `ON DELETE RESTRICT` (`409 template_in_use`, also when a `PUT` drops a
mode a profile uses; `422 template_identity_mismatch` on profiles). Arming
or starting a deployment copies the template (content, name, etag; the
built-in too) and the merged variables into the deployment, so later edits
and server upgrades don't change it.

**Previews**: `POST /unattend-templates/{id}/render` and
`POST /unattend-templates/render` (an unsaved draft) return JSON with the
content and findings, not bare XML. The agent and its plan don't change.

## Consequences
- Custom templates are as capable as the built-in one but carry the burden
  of identity themselves; the checks catch the omissions that would
  otherwise only show at sign-in (a join that silently didn't happen).
- Copies of the built-in template drift from it; the UI should say which
  template a profile uses and that copies aren't updated.
- Templates can't use arbitrary secrets yet.
