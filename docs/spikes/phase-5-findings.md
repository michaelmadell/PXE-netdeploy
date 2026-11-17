# Phase 5: findings

Measured behaviour and facts established while building Phase 5 (from
2026-11-16). Earlier results: [phase-4-findings.md](phase-4-findings.md).
Decisions: ADR 0027 onwards.

## Unattend templates (decision 1, ADR 0027)

**Automated, 2026-11-16:**

- The built-in template's output is pinned byte for byte against the Phase 4
  unattend for seven profiles (none, none with locales and EULA shown, local
  with two accounts and auto-logon, local with OOBE shown, AD credentials
  with OU and break-glass on arm64, AD credentials by UPN, AD offline join):
  `internal/unattend/testdata/builtin`, `TestBuiltinGolden`. Rewriting the
  built-in unattend as a template didn't change a byte.
- Escaping added to every output action matches the old explicit `x` calls
  exactly (same `xml.EscapeText`), including `"` as `&#34;`.
- Previews never read secrets: rendering every sample with a real secret
  value supplied leaves no trace of it (`TestPreviewMasksSecrets`).
- Server, end to end (`TestUnattendTemplates`): CRUD, the built-in template
  read-only, save refusals, draft and saved previews, profile references,
  `missing_variable` on arming, and the agent getting the deployment's copy
  after the template was edited, with the `windowsPE` pass removed.

**Lab, 2026-11-17, `nd-client-01`** (second lab host, Secure Boot, 2023
boot manager):

| Run | Result |
|---|---|
| Server started on the existing `C:\netdeploy\data` | Migration 0012 (profiles table rebuilt with the template foreign key) applied; profiles, clients and deployments intact |
| `local` profile with auto-logon, a copy of the built-in template plus a `FirstLogonCommands` entry, `requiredVariables: [site]`, client variable `site = lab` | **Worked.** Setup finished unattended, `labadmin` signed in, `C:\netdeploy-template.txt` said `site lab …`; `Panther\unattend.xml` deleted after Setup |
| Same profile switched back to the built-in template (`unattendTemplateId: builtin`, stored as null) | **Worked.** Same as the Phase 4 local runs |

Setup took the extra `FirstLogonCommands` element after `AutoLogon` in
oobeSystem's `Microsoft-Windows-Shell-Setup`; element order there isn't
WSIM's alphabetical order and Setup doesn't mind (the built-in template
already wrote `OOBE`, `UserAccounts`, `AutoLogon`).

Not lab-tested: an AD join through a custom template (the save checks
require the join for both methods); the `windowsPE` pass removal on a real
deployment (unit- and server-tested).
