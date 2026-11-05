# netdeploy: project brief

Read this first. It is the starting context for any new working session (human
or AI). It covers where the project stands and how we work; the *why* behind
each decision lives in the ADRs, and the *what* in the spec.

| For | Read |
|---|---|
| Components, boot flows, roadmap | [architecture.md](architecture.md) |
| Why each decision was made | [adr/](adr/README.md) |
| The API (source of truth) | [../api/openapi.yaml](../api/openapi.yaml) |
| What to build next | [plan/phase-1-spike.md](plan/phase-1-spike.md) |
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
| 0010 | Unknown PXE clients are ignored by default. |
| 0011 | Vite + React + Tailwind v4 + shadcn/ui (not Turbopack, not DaisyUI). |

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

## Next step

**Phase 1 spike**: boot Windows 11 WinPE under Secure Boot on the Hyper-V lab
VM from netdeployd's own DHCP and TFTP, then repeat on real I210 hardware. Full
plan, tasks and success criteria in [plan/phase-1-spike.md](plan/phase-1-spike.md).

## Open questions and things to verify

| Item | How it gets answered |
|---|---|
| Path bootmgr requests for the BCD when booted directly over PXE | Spike: log every TFTP request |
| DHCP broadcast reception on Windows with a socket bound to the interface IP | Spike |
| Whether Hyper-V's Secure Boot templates trust the Windows UEFI CA 2023 | Spike: try the 2023-signed boot manager |
| Where recent Win11 media keeps the 2023-signed boot manager (`EFI_EX`) | Spike: inspect current ISO and its WIMs |
| UEFI HTTP Boot of a RAM-disk ISO containing bootmgr + BCD + boot.wim | Spike on QEMU/OVMF or real hardware (Hyper-V likely has no HTTP Boot) |
| TFTP `windowsize` support in the chosen Go TFTP library | Spike; implement our own if missing |
| iPXE's Microsoft-signed shim (signed Nov 2025) for the non-Windows path | Phase 6 |
| The generated code checks that parameters have the right type but not ranges, patterns or enums, so add the kin-openapi request validator | |
| Raw Go error text appears in problem `detail` fields | |

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