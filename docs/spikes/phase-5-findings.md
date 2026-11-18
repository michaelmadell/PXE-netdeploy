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

## OS drivers need the Setup image (ADR 0022 amendment)

**Symptom, lab 2026-11-17:** with OS driver groups on the profile (Intel
chipset, 101 INFs), every deployment from a boot image built from boot.wim
index 1 failed at `injecting-drivers`: `dism.exe: exit status 0x80004002`.
dism.log: DISM copies itself from the target (`Copying DISM from
"W:\Windows\System32\Dism"`), starts `dismhost.exe`, and creating the
DismHostManager remote object fails with `0x80004002` (E_NOINTERFACE).

What was tried, all on index 1, all failing the same way:

| Attempt | Result |
|---|---|
| WinPE's own `dism.exe`, `/ScratchDir` on the target disk | 0x80004002 |
| The target image's own `W:\Windows\System32\Dism.exe` | 0x80004002 |
| DISM from the media (`sources\` of the install image), sent with the deployment | 0x80004002 (reverted) |
| Windows Server 2025 media instead of Windows 11 | 0x80004002 |

Control, on the lab host: host DISM 26100.9549 added all 101 drivers to the
same 26300 image mounted offline. The drivers and the DISM version weren't
the problem.

**Cause:** the WinPE environment. Plain WinPE (index 1) can't run DISM
offline servicing at all; index 2 (Windows Setup) can. Index 2 differs by the
WinPE-Setup, WinPE-Setup-Client and imagebasedsetup-media packages, most
likely COM/registry registrations DismHost needs.

**Fix:** index 2 with WinPE's own DISM, one `/Add-Driver /Recurse` run.
Lab, Windows 11 Pro from index 2: **succeeded**, 101 drivers added, Setup
finished. Setup's own program didn't start (the agent's `winpeshl.ini`
replaces the shell), so the Phase 1 WDS block doesn't apply.

Also from the investigation: the agent now flushes its log before reporting
failure and copies `agent.log` to `W:\Windows\Temp\netdeploy-agent.log`
when the target is mounted, and keeps DISM's log at
`W:\Windows\Temp\netdeploy-dism.log` (the last 20 error lines are sent
with the failure). wimlib's customise step now updates the exported WIM in
place and exports again, avoiding the Windows rename that failed with
"Resource busy".

**Automated, 2026-11-18:** `TestHasSetupPackage` (package found in a Setup-
shaped WIM, not in plain WinPE), `TestSourceIndexDefault` (header boot index
2 chosen on a two-image boot.wim), `TestBuildLifecycle` (plain WinPE build
records `canAddOsDrivers: false` with the warning; adopting carries it),
`TestBootImageCannotAddDrivers` (validate error, arming refused, warning
while unknown, nothing without OS drivers).

## Remaining UI (decision 4, UI passes 4 to 6)

**Automated, 2026-11-18:** `TestAdminCertificateInfo` (generated until a
valid custom pair is stored), `TestCertificateWarning` (30 days, expired,
renewal advice), `TestAgentReachable` (address and port, `advertisedAddress`,
DNS names by port, no HTTPS yet); `tsc` and the Vite build.

**Lab, 2026-11-18** (the user, against the restarted server):

| Check | Result |
|---|---|
| Admin certificate: separate files, a combined PEM, a mismatched pair, a 10-day certificate, back to the generated one | **Worked.** Mismatched pair refused; "Expires soon" and `admin_certificate_expiring` shown; a combined file fills both boxes (after a fix: the certificate box hid a certificate identical to the one in use) |
| A backup with Back up now | **Worked** |
| Settings tabs, deployment timeouts, DHCP leases on Clients | **Worked** |
| Overview readiness from `validate` and the Profiles list | **Worked** |

