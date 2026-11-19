# netdeploy: project brief

Read this first. It is the starting context for any new working session (human
or AI). It covers where the project stands and how we work; the *why* behind
each decision lives in the ADRs, and the *what* in the spec.

| For | Read |
|---|---|
| Running the server in the lab | [dev/running-the-lab.md](dev/running-the-lab.md) |
| Components, boot flows, roadmap | [architecture.md](architecture.md) |
| Why each decision was made | [adr/](adr/README.md) |
| The API (source of truth) | [../api/openapi.yaml](../api/openapi.yaml) |
| What to build next | [plan/phase-6-handover.md](plan/phase-6-handover.md) |
| What Phase 1 measured | [spikes/phase-1-findings.md](spikes/phase-1-findings.md) |
| What Phase 2 lab runs measured | [spikes/phase-2-findings.md](spikes/phase-2-findings.md) |
| What Phase 6 measured | [spikes/phase-6-findings.md](spikes/phase-6-findings.md) |
| Setup and dev workflow | [dev/getting-started.md](dev/getting-started.md) |
| Test lab | [dev/test-environments.md](dev/test-environments.md) |

## What we're building

A WDS-style network deployment framework for Windows 11 (other OSs later) that
works on stock Secure Boot hardware using Microsoft-signed boot managers (2011
and 2023 CA chains). Single Go binary with an embedded React UI and a
spec-first REST API, running on Windows or Linux hosts.

Features in scope: image import, driver packs, custom boot images with drivers
injected, unattend templating, profiles, client management, deployments via a
WinPE agent (or SMB + setup.exe), built-in DHCP (off / proxy / full), TFTP,
HTTP and HTTPS content, and identity modes (none, local accounts, AD, Entra).

## Context that shapes decisions

- **Developer environment:** Windows primarily, Linux as well. Scripts must run
  on both (PowerShell 7 + `just`).
- **Deployment hosts:** both Windows and Linux. Anything Windows-only (DISM,
  bcdedit, DPAPI) needs a Linux counterpart or a clear capability flag.
- **Target hardware includes devices with an Intel I210 NIC.** We assumed
  WinPE couldn't use it without an injected driver, which made boot image
  building core. Tested 2026-11-18: WinPE from the Windows 11 26H2 media
  (both boot.wim indexes) has an inbox driver and deploys an I210 machine
  as it is. Boot image
  building stays core for other NICs and storage controllers (PERC, RST VMD).
- **The developer wants guidance through design work**, especially the API spec,
  plus thorough documentation as the project goes.

## How we work

- **Spec first.** API changes start in `api/openapi.yaml`, then `just gen`, then
  implementation. Generated code is committed; CI fails on drift.
- **Propose, then apply.** For design work, lay out the recommended changes and
  the reasoning, get agreement, then edit the files. Go one area at a time.
- **Record decisions.** Anything a future reader would question gets an ADR.
  Reversals get a new ADR that supersedes the old one.
- **No co-author trailers.** Commit messages in this repo never include
  `Co-Authored-By` lines (or any other AI attribution).
- **Be explicit about what's verified.** Mark things as tested, untested, or
  "to verify in a spike". Several items below are still unverified.

## Decisions so far

Each has an ADR in `docs/adr/` with context and consequences.

| # | Decision |
|---|---|
| 0002 | Windows boots Microsoft's `bootmgfw.efi` directly, no shim. Shim/iPXE only for other OSs later. |
| 0003 | Go for the server and the WinPE agent. |
| 0004 | Spec-first, OpenAPI **3.0.3** (oapi-codegen doesn't support 3.1). |
| 0005 | Two boot image builders: DISM (Windows hosts) and wimlib (any host). |
| 0006 | Each boot image build produces its own BCD; TFTP picks one per client. |
| 0007 | Agent install by default, SMB + setup.exe optional; integrate with existing shares. |
| 0008 | Secrets are write-only, encrypted at rest, decrypted only for an agent. |
| 0009 | Separate admin (HTTPS, localhost by default) and content listeners. |
| 0010 | ~~Unknown PXE clients are ignored by default.~~ Superseded by 0013. |
| 0011 | Vite + React + Tailwind v4 + shadcn/ui (not Turbopack, not DaisyUI). |
| 0012 | Our own read-only TFTP server (pin/tftp lacks RFC 7440 windowsize). |
| 0013 | Every PXE client boots WinPE. Armed deployments run after a countdown; otherwise a password prompt (one global password, debug switch to disable) times out to the local disk, so there are no re-image loops. |
| 0014 | SQLite store: settings as one JSON document, STRICT tables with the invariants in the schema, embedded forward-only migrations, hand-written SQL. |
| 0015 | Content-addressed store: blobs named by SHA-256 under `<data>/content` (`-content` overrides), trees as manifests in SQLite served as a virtual `fs.FS`, mark-and-sweep GC, a free-space reserve. |
| 0016 | Jobs: the `jobs` table is the queue (one active job per resource), an in-process runner with lanes (builds 1, imports 2), throttled progress on `/events`, and no resume: a restart fails running jobs with `interrupted`. |
| 0017 | Uploads: own tus 1.0.0 server (creation, termination, expiration) ahead of the validator; hash computed while uploading; 64 GiB limit; imported uploads kept until deleted, others expire after 24 h. |
| 0018 | Media: own read-only UDF reader and WIM XML reader, `go-wim` for files inside `boot.wim`, `go-uefi` Authenticode with pinned Microsoft CAs for signers; no mounts or external tools for import. |
| 0019 | Driver packages: zip/7z/CAB (LZX via `expand.exe`/`cabextract`), own INF parser, catalog signature chained to pinned Microsoft roots plus a per-file hash check; `whql`/`vendor`/`unsigned`/`unknown`. |
| 0020 | Backups: scheduled (hourly/daily/weekly/monthly, local time) job; DB snapshot + certs + referenced blobs, incremental, retention 5; local folder or SMB share (pure-Go client); offline backup/restore commands. |
| 0021 | Secrets storage: AES-256-GCM under one data key wrapped by DPAPI (Windows) or a 0600 key file (Linux); key never in backups; passphrase export/import; `share-credential` kind. |
| 0022 | Building boot images: export one boot.wim index, add agent/config/`winpeshl.ini` (DISM also injects drivers; wimlib stages them for `drvload`), boot files from the built WIM, a content-store tree; wimlib preferred (1.3 s vs DISM's 43 s; required on Linux), DISM (inbox, elevated) otherwise, no ADK; storage drivers (PERC, RST VMD) load in WinPE with `drvload`; `inputHash` caching and reuse; stale computed on read; last good build keeps serving. Amended 2026-11-18: boot.wim's Setup image (index 2) by default, because plain WinPE can't run DISM offline servicing for OS drivers; builds record `canAddOsDrivers`. |
| 0023 | Own BCD writer (`internal/bcd`): REGF hive in Go with the Phase 1 RAM-disk elements, fixed GUID and timestamps (reproducible), no bcdedit/hivex; capabilities drop `bcdTool`. |
| 0024 | What a client boots: armed deployment's profile, else the client's, else the default; no fallback past a broken choice; sessions pin their trees against GC; no cache; profile storage brought forward; temporary `-tftp-root` flags removed. |
| 0025 | The agent and `/agent/*`: check-in (armed/locked; a running deployment whose client checks in again failed `restarted`), unlock (argon2id, rate-limited), hashed tokens, plan, `/agent/files/{sha256}` with Range; in WinPE: prompt, disk auto-select, diskpart, download + `dism /Apply-Image`, OS drivers, WinRE, `bcdboot` (`/bootex` for 2023), UEFI boot order from Go. |
| 0026 | Deployments: arming from the API with validation, profile and secret snapshots, timeouts (expiry, inactivity), graceful cancel, built-in unattend for none/local/AD (credentials or offline join in the unattend), computer name patterns. |
| 0027 | Unattend templates: whole-document Go templates over the built-in's data, auto XML-escaping, previews never decrypt (`********`), checks per identity mode on save and when arming, one built-in template in code (`builtin`), copied into each deployment. |
| 0028 | Content HTTPS: one internal CA (RSA-3072, ten years) made when HTTPS is first enabled, its key sealed with the secrets key and in backups; an in-memory server certificate for the HTTPS addresses and boot images' https hosts; `serverUrls` all http or all https; the agent trusts only the CA, at the later of its clock and the CA's start; CA fingerprint is a build input. Custom certificate and CA rotation deferred. |
| 0029 | Admin authentication: named local accounts (full admin, argon2id) with session cookies for the UI (`__Host-`, `HttpOnly`, `Secure`, `SameSite=Strict`, 8 h idle, 7 days max, Origin check) and `ndt_` bearer tokens for automation (hashed, shown once, can't manage accounts or tokens); first account from a setup code in the log and `setup-code.txt`; offline `admin reset-password`; health without auth is status only. |
| 0030 | Linux hosts: a non-root service account with `CAP_NET_BIND_SERVICE` and `CAP_NET_RAW`; privileges reported in capabilities and health (`missing_privileges`) instead of only bind failures; ping check falls back to Linux's unprivileged ICMP socket (`dhcp_ping_unavailable` otherwise); `ownerProcess` from `/proc` and the Windows owner tables; data directory permission checks; CI on Ubuntu with wimtools. iPXE and non-Windows OSs moved to a phase of their own. |
| 0031 | Packaging: an Inno Setup installer (LocalSystem service, private data folder, admin address and firewall asked at install, wimlib bundled, uninstall moves data aside) and a `.deb` (sysusers account, hardened unit with two capabilities, debconf admin address, purge moves data aside); versions from git tags; the generated admin certificate renewed at start; a `v*` tag publishes a GitHub release. Unsigned for now. |

Design details that live only in the spec (search `openapi.yaml` for them):
variable precedence (template < profile < client), idempotency keys and
`If-Match` etags, async jobs for imports and builds, image deduplication via the
import job, per-INF enable/disable on driver packages, profile and secret
snapshots at scheduling time, agent check-in trust model, per-interface DHCP
settings, atomic settings apply.

## Current status

**Done (Phase 0):** API spec drafted for all resources (45 paths, 78 schemas,
3.0.3). Repo skeleton: Go server serving the API and UI with `GET /system/health`
implemented, agent stub, React shell with a live health page, `justfile` and
PowerShell scripts, CI, Hyper-V lab script, docs and ADRs.

**Verified:**
* `just setup`, `just gen`, `just lint`, `just build` and `just dev` all work on Windows, and CI passes on Ubuntu and Windows.
* The generated identifiers in `server.go` match oapi-codegen v2.4.1, with `always-prefix-enum-values` making the names deterministic.
* The spec generates cleanly, and Redocly is clean apart from two intentional warnings recorded in `.redocly.lint-ignore.yaml`.
* Parameter errors return problem JSON.
* The Go toolchain is pinned to 1.27.3, because the gzipped embedded spec differs between Go versions and broke the drift check.

**Not yet verified:**

- The product name `netdeploy` (binary `netdeployd`) is still a working name.
  The Go module path is now `github.com/michaelmadell/pxe-netdeploy`.

**Done (Phase 1 spike, 2026-11-05):** Windows 11 WinPE boots under Secure
Boot on Hyper-V from netdeployd's own full-mode DHCP and TFTP, with both the
2011- and 2023-signed boot managers, served from a Windows host and from a
Linux host. `internal/dhcp` and `internal/tftp` (own server, ADR 0012) exist as
spike code behind flags. Results: [spikes/phase-1-findings.md](spikes/phase-1-findings.md).
Still open from Phase 1: HTTP Boot (task 9, optional). The physical I210
runs (tasks 7–8) passed on 2026-11-18, in Phase 6.

**Done (Phase 2, 2026-11-06):** the core server. Settings stored in SQLite
and applied atomically (`GET/PUT /settings`); DHCP full and proxy modes with
stored leases, reservations, ping check and the ADR 0013 filters; TFTP with
per-client content; a content HTTP listener per interface (ADR 0009); the
`/clients` API, `Idempotency-Key`, health and `/events`. Proxy mode booted
WinPE on Hyper-V with dnsmasq as the real DHCP server. Results:
[spikes/phase-2-findings.md](spikes/phase-2-findings.md). Content HTTPS moved
to Phase 6.

**Done (Phase 3, 2026-11-08):** storage and imports. Content-addressed store
(ADR 0015), jobs (ADR 0016), tus uploads (ADR 0017), ISO image import with
editions and the 2011- and 2023-signed boot managers (ADR 0018), driver
packages from zip/7z/CAB with INF parsing and catalog checks (ADR 0019),
backups to a folder or an encrypted SMB share with retention, scheduling and
offline backup/restore (ADR 0020), and secrets storage (ADR 0021, brought
forward from Phase 5). A 9 GB Win11 ISO uploads in 24 s and imports in 14 s;
the whole store backs up to an encrypted share in 31 s and restores in 27 s.
Results: [spikes/phase-3-findings.md](spikes/phase-3-findings.md).

**Done (Phase 4, 2026-11-08 to 2026-11-16):** boot images built with wimlib
or DISM (ADR 0022) with our own BCD writer (ADR 0023); each client boots its
profile's boot image (ADR 0024); the WinPE agent partitions, applies the
image, adds OS drivers, writes boot files and the firmware boot entry (ADR
0025); deployments are armed from the API or started at the boot prompt,
cancelled gracefully, timed out, and get a built-in unattend for no
accounts, local accounts, or an Active Directory join online or offline
(ADR 0026). All lab-tested on Hyper-V under Secure Boot with the 2023-signed
boot manager, including a real test domain. The web UI covers settings,
images, boot images, profiles (with accounts and domain), clients,
deployments (live progress and logs) and secrets. Deferred: the SMB install
method. Results: [spikes/phase-4-findings.md](spikes/phase-4-findings.md).

**Done (Phase 5, 2026-11-16 to 2026-11-18):** unattend templates (ADR
0027): whole-document Go templates over the built-in's data, previews that
never decrypt secrets, checks per identity mode, copied into each deployment;
lab-tested with a custom template and the built-in one. OS drivers: plain
WinPE (boot.wim index 1) can't run DISM offline servicing (`0x80004002`), so
boot images now default to the Setup image and validation refuses OS drivers
on a boot image that can't add them (ADR 0022 amendment); index 2 deployed
with 101 drivers. The web UI is complete for everything built: template
editor with live preview, driver packages and groups, Settings in tabs
(deployment timeouts, backups with snapshots, the admin certificate), DHCP
leases on Clients, and the Overview's readiness from `validate` with every
profile's status. Deferred, unscheduled: Entra/Autopilot (needs an Intune
tenant) and the SMB install method (only if needed). Results:
[spikes/phase-5-findings.md](spikes/phase-5-findings.md).

## Next step

**Phase 6: Linux and iPXE paths, content HTTPS, auth, packaging.** Start
from [plan/phase-6-handover.md](plan/phase-6-handover.md): what Phase 5
established, the decisions to make, and what carries over. Decision 1,
content HTTPS (ADR 0028), is built and lab-tested (2026-11-18). Decision 3,
admin authentication (ADR 0029), is built and lab-tested. Decision 4, now
the Linux host only (ADR 0030; iPXE and non-Windows OSs are a phase of their
own), is built and lab-tested on `nd-linux`. Decision 5, packaging (ADR 0031),
is built: the installer and `.deb`s build with `just package`; installing
them is the next lab step ([dev/running-the-lab.md](dev/running-the-lab.md),
"Installing from the packages"; checks in
[spikes/phase-6-findings.md](spikes/phase-6-findings.md)). Left in Phase 6:
decision 2, HTTPS boot (a spike first).

## Open questions and things to verify

| Item | How it gets answered |
|---|---|
| ~~Path bootmgr requests for the BCD when booted directly over PXE~~ | Answered: `\BCD`, then `\Boot\BCD` |
| ~~DHCP broadcast reception on Windows with a socket bound to the interface IP~~ | Answered: works |
| ~~Whether Hyper-V's Secure Boot templates trust the Windows UEFI CA 2023~~ | Answered: `MicrosoftWindows` does; `MicrosoftUEFICertificateAuthority` has no Windows CA at all |
| ~~Where recent Win11 media keeps the 2023-signed boot manager (`EFI_EX`)~~ | Answered: `Windows\Boot\EFI_EX\bootmgfw_EX.efi` inside boot.wim |
| UEFI HTTP Boot of a RAM-disk ISO containing bootmgr + BCD + boot.wim | Phase 1 task 9 (optional, not done): QEMU/OVMF or real hardware |
| ~~Final TFTP blksize/windowsize defaults; WinPE I210 driver need~~ | Answered 2026-11-18: a physical I210 machine boots and deploys with the 8192/8 defaults and no I210 driver (26H2 WinPE, either index, has one inbox); boot.wim over TFTP in about 12–15 s on 1 Gbit/s. [spikes/phase-1-findings.md](spikes/phase-1-findings.md) |
| ~~Proxy DHCP mode against real firmware~~ | Answered for Hyper-V (2026-11-06): boots; the firmware uses the proxy offer's boot file and skips :4011. [spikes/phase-2-findings.md](spikes/phase-2-findings.md) |
| ~~Option 97 byte order~~ | Answered: first three fields little-endian; `smbiosUuid` equals the VM's `BIOSGUID` |
| Proxy :4011 boot server path and `listenOn67: false` with real firmware | Firmware that does boot server discovery (physical hardware) |
| ~~ICMP ping check with raw sockets on Windows~~ | Answered: works from an elevated shell (`TestICMPPinger` passes on the Windows host) |
| ~~TFTP `windowsize` support in the chosen Go TFTP library~~ | Answered: not in `pin/tftp/v3`; own server, ADR 0012 |
| iPXE's Microsoft-signed shim (signed Nov 2025) for the non-Windows path | The non-Windows phase (moved out of Phase 6, ADR 0030) |
| ~~The generated code checks that parameters have the right type but not ranges, patterns or enums, so add the kin-openapi request validator~~ | Done (Phase 2 step 2): `server.RequestValidator`, 400 with per-field `errors[]` |
| Raw Go error text appears in problem `detail` fields | |
| ~~Real DISM and wimlib builds of the Win11 boot.wim; the 2023 boot manager with the `Fonts_EX` set; wimlib-added files without security descriptors in WinPE~~ | Answered 2026-11-08: both build and boot on `nd-win`; the agent starts ([spikes/phase-4-findings.md](spikes/phase-4-findings.md)) |

## Suggested skills for the next session

When working with Claude, these skills fit the upcoming work:

- `think-before-coding`, `error-handling-as-design`, `observability-by-default`
  before writing the DHCP, TFTP and content servers.
- `testing-with-discernment` when adding tests for protocol handling.
- `diagnosing-bugs` / `debugging-discipline` if setup or the spike hits problems.
- `modern-web-guidance` and `frontend-design` before building UI pages (the
  first attempt couldn't run `modern-web-guidance`; it needs network access).

## Uploading to a Claude project

To give a new chat full context, add these to the project's knowledge:
this file, `docs/architecture.md`, `docs/plan/phase-1-spike.md`,
`docs/adr/README.md`, and `api/openapi.yaml`. Add individual ADRs or source files
when a session works on them.