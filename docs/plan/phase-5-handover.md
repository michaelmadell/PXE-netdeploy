# Phase 5 handover

Written 2026-11-16 at the end of Phase 4. Gives a new session what it needs
to design and build Phase 5 without the Phase 4 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 5 of netdeploy. Read `PROJECT_BRIEF.md` and
> `phase-5-handover.md` in the project first, then `phase-4-findings.md`,
> `architecture.md`, `adr-index.md` (especially 0025 and 0026) and the
> relevant parts of `openapi.yaml`. The repo is my connected folder
> `PXE-Netdeploy`. Follow "How we work" in the brief: propose the design for
> one area at a time, get my agreement, then change the spec, then
> implement. Start with decision 1 in the handover (unattend templates).

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work (no co-author trailers in commits; give `git add` lines with commit commands) |
| This file | Phase 5 starting point |
| `docs/spikes/phase-4-findings.md` | Boot order, domain join, ODJ, console and export measurements |
| `docs/adr/0025-agent.md`, `0026-deployments.md` | The agent and deployments as built, with their amendments |
| `docs/adr/0007-agent-and-smb-install-methods.md`, `0008-secrets-model.md` | SMB method and secrets model |
| `docs/dev/running-the-lab.md` | Lab setup from scratch, arming, identity, AD join start to finish |
| `claude/ui-handover.md`, `ui-pass-2.md`, `ui-pass-3.md` (Claude project) | Web UI conventions and what each page does |
| `api/openapi.yaml` | `/unattend-templates`, `/smb-targets`, `IdentityEntra`, `AgentDeploymentPlan.identityFiles` |

## State at handover

Phases 1–4 are built and lab-tested. Phase 4 added:

| Package | What it does |
|---|---|
| `internal/bootimage`, `internal/bcd`, `internal/agentbin` | Boot image builds (wimlib or DISM), our own BCD writer, the agent embedded in the server |
| `internal/boot` (`Profiles`) | Which boot image each client gets (pending deployment's snapshot, client profile, default profile) |
| `internal/server/agent.go` | `/agent/*`: check-in, unlock, start, plan, progress (409 when cancelled), logs, complete, files, unattend |
| `internal/server/deployments.go`, `profilevalidate.go`, `sweep.go` | Arming, cancel, profile validation, expiry and inactivity timeouts |
| `internal/unattend` | Profile snapshot parsing, computer name patterns, the built-in unattend (none, local, AD credentials, ODJ `AccountData`) |
| `internal/agent/{client,deploy,disk,firmware,sysinfo,ui,wire}`, `cmd/agent` | The WinPE agent: disk choice, diskpart, download and DISM apply, OS drivers, unattend, WinRE, bcdboot, firmware boot entry and order, graceful cancel, progress keepalive |
| `web/src/pages/{clients,deployments,secrets,profiles}` | UI: clients, deployments (live progress, logs), secrets, profile identity and Windows settings |

Endpoints still "not implemented": `/unattend-templates`, `/smb-targets`,
`/tokens`. Profiles refuse `installMethod: smb`, `unattendTemplateId` and
identity `entra` with `422 not_implemented`.

## Things Phase 4 established that Phase 5 must respect

| Fact | Consequence for Phase 5 |
|---|---|
| bcdboot run from a network-booted WinPE writes no firmware boot entry | The agent writes it (ADR 0025). Any new install path (SMB/`setup.exe`) must keep the agent's boot-order step after Setup |
| WinPE has no `djoin.exe` | ODJ goes in the unattend (`UnattendedJoin/Identification/Provisioning/AccountData`). Templates must keep that; no WinPE-side identity tools |
| Setup retries a failed domain join for ~4.5 min, then finishes unjoined | A join failure shows only at sign-in. Templates that join need DNS to the DC from DHCP (validate warns `dhcp_no_dns`) |
| Setup won't create a local account named like a built-in one | Validation refuses `Administrator`, `Guest`, `DefaultAccount`, `WDAGUtilityAccount`; template-defined accounts need the same check |
| The unattend is rendered per request from the deployment's snapshot and secret copies, never stored; SetupComplete deletes it | Templates render the same way: inputs are the snapshot, the client, secret values; no stored rendered files |
| Computer name patterns: Go templates with `first`/`last`/`upper`/`lower`, a missing field refuses arming | Unattend templates should reuse the same template engine and data names (`.Client.*`, `.Variables`) |
| The agent's console in WinPE is 120×30 (logged when a deployment starts); the WinPE console font lacks the eighth-blocks (▏…▉) | Agent UI uses only `█▓▒░` and fits 120 columns |
| One edition is 93 % of the 26H2 `install.wim` | Don't revisit per-edition export; the SMB path is where a big saving could come from (no staging on the target disk) |
| A cancelled deployment's token works 30 min for logs and the acknowledgement | New agent calls during a cancel should go through the same grace window |

## Decisions to make first

### 1. Unattend templates (ADR 0007, spec `/unattend-templates`)

The spec has CRUD plus `POST /unattend-templates/{id}/render`, the
profile's `unattendTemplateId` and template `variableDefaults` and
`identityModes`. Settle: the template language (Go `text/template` over the
same data as the built-in one, or XML fragments merged into the built-in
unattend), how secrets reach a template without being printable in a
preview (`render` shows `********`), validation (well-formed XML, the passes
Setup needs, the identity mode's components present), and whether the
built-in unattend becomes the default template.

### 2. Entra / Autopilot (`IdentityEntra`)

Methods `autopilot-registered` (nothing injected), `autopilot-json` (file
into `Windows\Provisioning\Autopilot`) and `provisioning-package` (DISM
`/Add-ProvisioningPackage` offline). The agent needs these files: the
`identityFiles` plan field remains (its endpoint was removed with ODJ);
decide the endpoint again. Needs an Intune tenant to test.

### 3. SMB / `setup.exe` install method (ADR 0007; deferred from Phase 4)

Spike first, in the lab: does `setup.exe` from a share run in our
network-booted WinPE (Phase 1 saw the media's own Setup refuse WDS-style
boots); can the agent partition first and point Setup at `W:`; does
`setup.exe /noreboot` return control so the agent keeps its boot order and
completion steps; does `/installdrivers` take the OS drivers. Then: SMB
target storage, a publish job copying the media tree to the share, the
plan's `smb` section. Only if there's a need for it.

### 4. Remaining UI

Drivers (packages, groups, INF enable), unattend templates (editor and
preview), Settings gaps (`deployments` timeouts, backups, admin
certificate, DHCP leases), Overview's readiness from `validate`.

## Carried over

| Item | Status |
|---|---|
| Phase 1 tasks 7–8: physical I210 device and timing runs | Waiting for hardware; may change the TFTP defaults and whether WinPE needs the I210 driver |
| Phase 1 task 9: UEFI HTTP Boot of a RAM-disk ISO | Optional |
| Proxy DHCP :4011 boot server path, `listenOn67: false` | Untested with real firmware |
| Content HTTPS, generated CA, `/settings/content-ca` | Phase 6; the boot prompt password and agent traffic (plan, unattend with secrets) are in clear until then |
| Admin API authentication (`bearerAuth`) | Phase 6; admin binds to localhost by default |
| Prompt timeout's `BootNext` preferring an existing partition | Unit-tested; not watched in the lab |
| Stale firmware boot entries from before 2026-11-12 | Only removed by hand (`bcdedit /delete`, or `Set-VMFirmware -BootOrder`) |
| `ownerProcess` in bind failures | Always empty |
| Plain ISO9660 media (no UDF) | Refused until the Linux phase (ADR 0018) |
| `cloudsoda/go-smb2` | Pinned by commit; NTLM tested, Kerberos not |
| Backups don't include `secrets.key` by design | Operators must run `netdeployd key export` once |
| Web bundle over 500 kB | Vite warns; code-split routes when convenient |

## Lab and environment

- Second lab host (from 2026-11-11): repo at `D:\PXE-Netdeploy`, server data
  at `C:\netdeploy\data` (`just dev -DataDir C:\netdeploy\data`; D: is
  small). Go 1.27.3, PowerShell 7, `just`, pnpm, wimlib in `.tmp\wimlib`.
- Hyper-V: `NetDeployLab` internal switch, host `10.77.0.1/24`;
  `nd-client-01` (MAC `00:15:5d:c8:79:09`, 80 GB disk); the test domain
  `michael-ad.test` (NetBIOS `MICHAEL-DOM`), DC `MICHAEL-AD` with a second
  adapter on the lab switch at `10.77.0.10`, handed out as DNS by the lab
  DHCP; `OU=Lab,DC=michael-ad,DC=test`.
- Media: `Win 11 26H2.iso` (7.6 GB `install.wim`, 11 editions).
- The desktop shell Claude uses runs unelevated; the server and Hyper-V
  commands are run elevated by the user. `vmconnect` can be slow to attach:
  open it before starting the VM.
- Reading a VM's Windows logs without signing in: VM off, `Mount-VHD`
  (read-write, for a drive letter), the largest partition,
  `Windows\debug\NetSetup.LOG`, `Windows\Panther\UnattendGC\setupact.log`.

## Gotchas met in Phase 4

- `Invoke-RestMethod -Body <hashtable>` sends form fields, not JSON; always
  `ConvertTo-Json` (the `api` helper in the lab guide does). In PowerShell,
  `"…"` expands `$`; put passwords in `'…'`.
- `GET /clients/{id}` carries its ETag in the body (`etag`), not a header.
- A client first seen by DHCP has no serial until the agent checks in; name
  patterns using it were refused until then (fixed: check-in fills it).
- Go files written from a shell heredoc can pick up a literal U+FEFF; Go
  refuses a BOM mid-file. Write escapes (`"\uFEFF"`).
- The Linux shell on the connected folder can't delete files; use the
  Windows shell (`Remove-Item`) or move to `_to_delete/`.
