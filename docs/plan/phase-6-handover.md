# Phase 6 handover

Written 2026-11-18 at the end of Phase 5. Gives a new session what it needs
to design and build Phase 6 without the Phase 5 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 6 of netdeploy. Read `PROJECT_BRIEF.md` and
> `phase-6-handover.md` in the project first, then `phase-5-findings.md`,
> `architecture.md`, `adr-index.md` (especially 0009, 0021, 0022 and 0025)
> and the relevant parts of `openapi.yaml`. The repo is my connected folder
> `PXE-Netdeploy`. Follow "How we work" in the brief: propose the design for
> one area at a time, get my agreement, then change the spec, then
> implement. Start with the decision I choose from the handover.

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work (no co-author trailers in commits; give `git add` lines with commit commands) |
| This file | Phase 6 starting point |
| `docs/spikes/phase-5-findings.md` | Unattend templates in the lab; why OS drivers need boot.wim's Setup image |
| `docs/adr/0009-separate-admin-and-content-listeners.md` | Separate admin and content listeners: the base for content HTTPS and auth |
| `docs/adr/0021-secrets-storage.md` | DPAPI on Windows, a 0600 key file on Linux |
| `docs/adr/0022-building-boot-images.md` | Builders, and the 2026-11-18 amendment (Setup image by default) |
| `docs/adr/0025-agent.md`, `0027-unattend-templates.md` | The agent, its config and what travels to it |
| `docs/dev/running-the-lab.md` | Lab setup from scratch through to OS drivers and the Setup image |
| `claude/ui-handover.md`, `ui-pass-4.md` to `ui-pass-6.md` (Claude project) | Web UI conventions and the pages built in Phase 5 |
| `api/openapi.yaml` | `bearerAuth`, `/tokens`, `contentHttps`, `services.https`, `/settings/content-ca` |

## State at handover

Phases 1–5 are built. Phase 5 added:

| Package | What it does |
|---|---|
| `internal/unattend` | Templates (`template.go`, `check.go`, `render.go`), the built-in template in code, sample data for previews |
| `internal/server/unattend.go`, `internal/store/unattend.go` | `/unattend-templates` CRUD and `render`; migration 0012 |
| `internal/bootimage` | Setup-image default (`SourceIndex` from the WIM header), `canAddOsDrivers` from the WinPE-Setup package; recipe 2; migration 0013 |
| `internal/server/profilevalidate.go` | New findings: `boot_image_cannot_add_drivers`, `agent_unreachable`, template checks |
| `internal/settings/certinfo.go`, `endpoints.go` | `admin.certificateInfo`; the HTTP endpoints booted machines can reach |
| `web/src/pages/{templates,drivers,settings}`, `Settings.tsx`, `clients/Leases.tsx`, `components/Readiness.tsx` | Template editor (CodeMirror, live preview), driver packages and groups, Settings in tabs, DHCP leases, Overview readiness from `validate` |

Still refused or not implemented: `/smb-targets`, `/tokens`, profiles with
`installMethod: smb` or identity `entra` (`422 not_implemented`),
`services.https` and `/settings/content-ca` (`404 content_https_not_available`),
`optionalComponents` and `produceHttpBootIso` on boot images.

## Things Phase 5 established that Phase 6 must respect

| Fact | Consequence for Phase 6 |
|---|---|
| Plain WinPE (boot.wim index 1) can't run DISM offline servicing; the Setup image (index 2) can | Keep the Setup image as the default on every host. A Linux build path must also produce a Setup-image WinPE; check that wimlib's export of index 2 behaves the same |
| The agent's `config.json` already has `contentCaPem` (empty) | Content HTTPS: put the generated CA there at build time; a CA change makes boot images stale (an input to `inputHash`) |
| The unattend (with secrets) and the boot prompt password travel in clear over the content listener | Content HTTPS is what fixes this; until then the brief says so |
| The admin certificate is hot-swapped (`GetCertificate`); the generated one lasts two years and isn't renewed | Auth and packaging should decide renewal; health warns `admin_certificate_expiring` 30 days ahead |
| Validation is the single source of "can this deploy" (arming, boot prompt start, Profiles, Overview) | New checks (HTTPS, auth, Linux capabilities) belong in `validateProfile` so every caller gets them |
| The web bundle sits just under Vite's 500 kB warning (480 kB) | Lazy-load new pages, as Phase 5 did for Settings, Profiles, Secrets, drivers and templates |

## Decisions to make first

### 1. Content HTTPS (ADR 0009; spec `contentHttps`, `services.https`, `/settings/content-ca`)

A generated internal CA and per-interface server certificates (or a custom
pair), the agent trusting the CA from `config.json`, HTTPS for `/agent/*`
and `/content/boot/*`. Settle: CA lifetime and storage (with the secrets key
or beside the admin certificate; in backups?), certificate names (interface
addresses, `advertisedAddress`), whether HTTP stays on for TFTP-era Boot
Manager downloads (Boot Manager itself can't do HTTPS), and the boot image
staleness that follows a CA change.

### 2. HTTPS boot (UEFI HTTP Boot over TLS) and the HTTP Boot ISO

> 2026-11-20: built (ADR 0032). The NUC's firmware runs EFI programs only,
> so HTTP(S) Boot goes through the signed iPXE shim, iPXE and wimboot rather
> than a RAM-disk ISO. The ISO is kept for later.

Phase 1 task 9 (HTTP Boot of a RAM-disk ISO) was never run; `produceHttpBootIso`
is refused. Firmware must trust the CA (`/settings/content-ca` as DER for
enrolment). Spike first on QEMU/OVMF or real hardware.

### 3. Admin API authentication (`bearerAuth`, `/tokens`)

Tokens (hashed, shown once), how the first one is created (a CLI command, as
with `key export`), the UI's sign-in, and whether the localhost-only default
stays. The agent API already has its own tokens (ADR 0025).

### 4. Linux host and iPXE paths

> 2026-11-19: narrowed to the Linux host (ADR 0030). iPXE with the signed
> shim, ISO9660 media and non-Windows install flows move to a phase of their
> own.

DHCP/TFTP on Linux were proven in Phase 1; Phase 6 makes the whole server
run there: wimlib-only builds, the 0600 secrets key (ADR 0021), plain
ISO9660 media (refused until now, ADR 0018), raw-socket ping. iPXE with
Microsoft's signed shim (Nov 2025) for non-Windows OSs.

### 5. Packaging

> 2026-11-19: built (ADR 0031): an Inno Setup installer and a `.deb`.

Windows service and Linux systemd unit, an installer or archive layout,
upgrades (migrations run at start), the wimlib licence question before
bundling it (ADR 0022), versioning shown in health and the UI.

### Deferred from Phase 5 (unscheduled)

- **Entra / Autopilot** (`IdentityEntra`: `autopilot-registered`,
  `autopilot-json`, `provisioning-package`). Needs an Intune tenant. The
  `identityFiles` plan field remains; decide its endpoint again.
- **SMB / `setup.exe` install** (ADR 0007). Spike first; only if there's a
  need. Note the Setup image is now the default boot image, which may help
  `setup.exe` run from WinPE.

## Carried over

| Item | Status |
|---|---|
| ~~Phase 1 tasks 7–8: physical I210 device and timing runs~~ | Done 2026-11-18: deployed over HTTPS with the default TFTP settings and no I210 driver (inbox in 26H2 WinPE, both indexes); boot.wim over TFTP in about 12–15 s |
| Proxy DHCP :4011 boot server path, `listenOn67: false` | Untested with real firmware |
| Prompt timeout's `BootNext` preferring an existing partition | Unit-tested; not watched in the lab |
| Stale firmware boot entries from before 2026-11-12 | Only removed by hand |
| ~~`ownerProcess` in bind failures~~ | Filled from `/proc` (Linux) and the TCP/UDP owner tables (Windows), ADR 0030 |
| `cloudsoda/go-smb2` | Pinned by commit; NTLM tested, Kerberos not |
| Backups don't include `secrets.key` by design | The UI and health say to run `netdeployd key export` once |
| Raw Go error text in problem `detail` fields | Open |
| Not lab-tested in Phase 5 | An AD join through a custom template; the `windowsPE` pass removal on a real deployment; `agent_unreachable` seen on a real profile (unit-tested) |

## Lab and environment

- Second lab host: repo at `D:\PXE-Netdeploy`, server data at
  `C:\netdeploy\data` (`just dev -DataDir C:\netdeploy\data`). Go 1.27.3,
  PowerShell 7, `just`, pnpm, wimlib in `.tmp\wimlib`.
- Hyper-V: `NetDeployLab` internal switch, host `10.77.0.1/24`;
  `nd-client-01` (MAC `00:15:5d:c8:79:09`, 80 GB disk); the test domain
  `michael-ad.test` (NetBIOS `MICHAEL-DOM`), DC at `10.77.0.10` on the lab
  switch; `OU=Lab,DC=michael-ad,DC=test`.
- Media: `Win 11 26H2.iso`; boot images should use boot.wim index 2.
- Test admin certificates: `.tmp\make-cert.ps1` (PowerShell 7, .NET, no
  OpenSSL) writes `cert.pem`, `key.pem`, `combined.pem`; `-Days 10` for the
  expiry warning.
- The desktop shell Claude uses runs unelevated; the server and Hyper-V
  commands are run elevated by the user.

## Gotchas met in Phase 5

- DISM offline servicing in plain WinPE fails with `0x80004002` whatever DISM
  binary is used (the image's, the media's, WinPE's own): the environment,
  not DISM, is the problem. Logs: `W:\Windows\Temp\netdeploy-dism.log` and
  `netdeploy-agent.log` on the target after a failure.
- wimlib on Windows can't rename over a file it just wrote ("Resource busy"):
  update in place, then export.
- oapi-codegen makes fields with a `default` required in the generated
  TypeScript too; fill defaults in the UI rather than treating them as optional.
- Redocly rejects unquoted flow-mapping descriptions containing `, ` or `:`;
  quote them.
- In inline PowerShell commands sent through the desktop shell, `$` is
  expanded by the outer shell; put scripts in `.tmp\*.ps1` and run them with
  `pwsh -NoProfile -File`.
- Go test caching can report `(cached)` after an earlier run you didn't see;
  use `-count=1` when it matters.
