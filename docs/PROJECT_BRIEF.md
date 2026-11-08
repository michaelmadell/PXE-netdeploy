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
| What to build next | [plan/phase-3-handover.md](plan/phase-3-handover.md) |
| What Phase 1 measured | [spikes/phase-1-findings.md](spikes/phase-1-findings.md) |
| What Phase 2 lab runs measured | [spikes/phase-2-findings.md](spikes/phase-2-findings.md) |
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
- **Target hardware includes devices with an Intel I210 NIC** that WinPE can't
  use without an injected driver. This is why boot image building is core, not
  optional.
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
| 0022 | Building boot images: export one boot.wim index, add agent/config/`winpeshl.ini` (DISM also injects drivers; wimlib stages them for `drvload`), boot files from the built WIM, a content-store tree; wimlib preferred (1.3 s vs DISM's 43 s; required on Linux), DISM (inbox, elevated) otherwise, no ADK; storage drivers (PERC, RST VMD) load in WinPE with `drvload`; `inputHash` caching and reuse; stale computed on read; last good build keeps serving. |
| 0023 | Own BCD writer (`internal/bcd`): REGF hive in Go with the Phase 1 RAM-disk elements, fixed GUID and timestamps (reproducible), no bcdedit/hivex; capabilities drop `bcdTool`. |
| 0024 | What a client boots: armed deployment's profile, else the client's, else the default; no fallback past a broken choice; sessions pin their trees against GC; no cache; profile storage brought forward; temporary `-tftp-root` flags removed. |
| 0025 | The agent and `/agent/*`: check-in (armed/locked; a running deployment whose client checks in again failed `restarted`), unlock (argon2id, rate-limited), hashed tokens, plan, `/agent/files/{sha256}` with Range; in WinPE: prompt, disk auto-select, diskpart, download + `dism /Apply-Image`, OS drivers, WinRE, `bcdboot` (`/bootex` for 2023), UEFI boot order from Go. |

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
Still open from Phase 1: physical I210 hardware (tasks 7–8, waiting for
hardware) and HTTP Boot (task 9, optional).

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

**In progress (Phase 4, from 2026-11-08):** decisions 1–3: building a boot image
(ADR 0022: `/boot-images`, the build job, both builders, the agent embedded
in the server), writing its BCD (ADR 0023, `internal/bcd`), deciding what
each client boots (ADR 0024: `boot.Profiles`, `/profiles` storage), and the
agent (ADR 0025: `/agent/*`, `internal/agent/…`).
Results: [spikes/phase-4-findings.md](spikes/phase-4-findings.md).

## Next step

**Phase 4: boot image builder, agent, profiles, deployments.** Start from
[plan/phase-4-handover.md](plan/phase-4-handover.md): it lists what Phase 3
established, the decisions to make first (building a boot image, writing the
BCD, the real `Booter`, the agent, profiles and deployments), and what
carries over.

## Open questions and things to verify

| Item | How it gets answered |
|---|---|
| ~~Path bootmgr requests for the BCD when booted directly over PXE~~ | Answered: `\BCD`, then `\Boot\BCD` |
| ~~DHCP broadcast reception on Windows with a socket bound to the interface IP~~ | Answered: works |
| ~~Whether Hyper-V's Secure Boot templates trust the Windows UEFI CA 2023~~ | Answered: `MicrosoftWindows` does; `MicrosoftUEFICertificateAuthority` has no Windows CA at all |
| ~~Where recent Win11 media keeps the 2023-signed boot manager (`EFI_EX`)~~ | Answered: `Windows\Boot\EFI_EX\bootmgfw_EX.efi` inside boot.wim |
| UEFI HTTP Boot of a RAM-disk ISO containing bootmgr + BCD + boot.wim | Phase 1 task 9 (optional, not done): QEMU/OVMF or real hardware |
| Final TFTP blksize/windowsize defaults; WinPE I210 driver need | Phase 1 tasks 7–8 on physical hardware |
| ~~Proxy DHCP mode against real firmware~~ | Answered for Hyper-V (2026-11-06): boots; the firmware uses the proxy offer's boot file and skips :4011. [spikes/phase-2-findings.md](spikes/phase-2-findings.md) |
| ~~Option 97 byte order~~ | Answered: first three fields little-endian; `smbiosUuid` equals the VM's `BIOSGUID` |
| Proxy :4011 boot server path and `listenOn67: false` with real firmware | Firmware that does boot server discovery (physical hardware) |
| ~~ICMP ping check with raw sockets on Windows~~ | Answered: works from an elevated shell (`TestICMPPinger` passes on the Windows host) |
| ~~TFTP `windowsize` support in the chosen Go TFTP library~~ | Answered: not in `pin/tftp/v3`; own server, ADR 0012 |
| iPXE's Microsoft-signed shim (signed Nov 2025) for the non-Windows path | Phase 6 |
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