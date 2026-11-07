# Phase 3 handover

Written 2026-11-06 at the end of Phase 2. Gives a new session what it needs
to design and build Phase 3 without the Phase 2 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 3 of netdeploy. Read `PROJECT_BRIEF.md` and
> `phase-3-handover.md` in the project first, then `phase-2-findings.md`,
> `architecture.md`, `adr-index.md` and the relevant parts of `openapi.yaml`.
> The repo is my connected folder `PXE-Netdeploy`. Follow "How we work" in the
> brief: propose the design for one area at a time, get my agreement, then
> change the spec, then implement. Start with decision 1 in the handover
> (content store layout).

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work (no co-author trailers in commits) |
| This file | Phase 3 starting point |
| `docs/spikes/phase-2-findings.md` | Proxy DHCP on Hyper-V, full-mode regression, decisions made in lab |
| `docs/spikes/phase-1-findings.md` | Boot Manager, WIM and Secure Boot facts that shape imports |
| `docs/architecture.md` | Components, boot flows, roadmap |
| `docs/adr/README.md` | Decisions 0001–0014 |
| `api/openapi.yaml` | `/uploads` (tus), `/images`, `/driver-packages`, `/driver-groups`, `/jobs`, `Image`, `DriverPackage`, `Job` |
| `docs/dev/running-the-lab.md` | How to run the server and the lab now (settings, not flags) |

## State at handover

All of Phase 2 (core server) is built and tested; see the handover for
Phase 2 and the findings for lab results.

| Package | What it does |
|---|---|
| `internal/store` | SQLite (ADR 0014): settings document, clients, deployments, DHCP leases, idempotency keys; embedded forward-only migrations with a `VACUUM INTO` snapshot |
| `internal/settings` | Stored settings, validation against the host, `GET/PUT /settings`, building the listener plan; boot prompt password hashing |
| `internal/netsvc` | Owns every socket (admin HTTPS, DHCP, TFTP, content HTTP); atomic apply, sockets kept across changes |
| `internal/dhcp` | Full and proxy modes, stored leases, reservations, ping check, ADR 0013 filters and sightings |
| `internal/boot` | `Booter` (what a client boots; `Static` until Phase 4) and boot sessions (client IP to MAC) |
| `internal/tftp` | Own server (ADR 0012), per-client content via `ClientRoot`, `\BCD` never served |
| `internal/content` | Content HTTP listener: `/content/boot/` per client, the agent API, nothing else |
| `internal/events` | In-memory bus for `GET /events` (SSE) |
| `internal/server` | API handlers: settings, health, clients, leases, events; request validator, Idempotency-Key |

Endpoints implemented: `/system/health`, `/settings`, `/settings/content-ca`
(404 until Phase 6), `/clients`, `/dhcp/leases`, `/events`. Everything else
answers 500 "not implemented yet".

Temporary: `-tftp-root`, `-boot-file`, `-http-boot-url` stand in for boot
images until Phase 4 (boot images are built in Phase 4, so these stay through
Phase 3).

## Things Phase 2 established that Phase 3 must respect

| Fact | Consequence for Phase 3 |
|---|---|
| Every POST honours `Idempotency-Key` (middleware, 24 h, 1 MiB body cap) | Import endpoints get replay for free; tus endpoints are not JSON, check the cap doesn't bite |
| The request validator rejects schema violations with per-field `errors[]` | Spec the import bodies precisely; don't re-validate shapes in handlers |
| Content is served per client from a directory (`boot.Answer.Root`) | The content store must be able to expose a boot image as a directory tree (or a resolver from path to blob) for TFTP and HTTP |
| `/events` exists; `job.updated` and `image.updated` are already in the enum | Jobs publish progress there, throttled (a few per second at most) |
| `Settings`, `clients`, etc. use the version as ETag | New resources follow the same pattern (`version` column, `If-Match`) |
| Store errors map to typed errors; constraints live in the schema | New tables (images, driver packages, jobs, uploads) carry their invariants as constraints |
| Admin API has no authentication yet (bound to localhost by default) | Uploads of multi-GB ISOs should still go through the admin listener; auth is Phase 6 |
| `Get-AuthenticodeSignature` reports catalog signatures (Phase 1) | Signer detection for boot managers and drivers must read the embedded signature |
| The 2023-signed boot manager is only inside `boot.wim` (`Windows\Boot\EFI_EX\bootmgfw_EX.efi`) | Image import must read inside the WIM, not just the ISO |

## Decisions to make first

### 1. Content store layout

`architecture.md` says "content-addressed files (SHA-256)". Settle:
where blobs live (`<data>/content/sha256/ab/cdef…`), how a tree (an extracted
ISO, a boot image's files) is described (a manifest table mapping path to
blob), reference counting and garbage collection, disk space checks before
an import, and how TFTP/HTTP serve a tree (a virtual `fs.FS` over the
manifest, or a materialised directory). Probably an ADR.

### 2. Reading ISOs and WIMs on both host OSs

Win11 ISOs are UDF. Options: a pure-Go UDF reader, mounting (`Mount-DiskImage`
on Windows, loop mount on Linux, both need privilege), or `7z`/`wimlib` as
external tools. For WIMs: wimlib (`wimlib-imagex`) on both, DISM on Windows
(ADR 0005 already names both builders), or a pure-Go WIM metadata reader for
the import step (list editions, extract one file such as `bootmgfw_EX.efi`).
Decide what import needs (metadata, editions, boot managers, hashes) versus
what the Phase 4 builder needs.

### 3. Jobs

`Job` exists in the spec. Decide: a `jobs` table, a runner with bounded
concurrency (one build at a time per host, per the spec), cancellation,
progress throttling onto `/events`, and what happens to a running job when
the server restarts (mark failed with `interrupted`, or resume).

### 4. Uploads (tus 1.0.0)

The spec routes uploads through tus. Decide: tusd as a library or our own
minimal tus server (creation, core, termination, expiry), where partial
uploads live (inside the content store's staging area), size limits, and
expiry of abandoned uploads.

### 5. Driver packs

Per-INF enable/disable is in the spec. Decide INF parsing scope (class,
architecture, hardware IDs, catalog file), signer detection (embedded
signature on the `.cat`), and how driver groups reference packages.

## Phase 3 scope

From the roadmap: storage and imports (ISOs, driver packs, jobs, uploads).
Suggested order: content store, jobs, uploads, image import, driver import.
Phase 4 (boot image builder, agent, profiles, deployments) depends on these;
keep its interfaces in mind (`boot.Booter` becomes the real ADR 0013 decision,
boot images become content trees) but don't build it.

## Carried over

| Item | Status |
|---|---|
| Phase 1 tasks 7–8: physical I210 device and timing runs | Waiting for hardware; sets final TFTP defaults |
| Phase 1 task 9: UEFI HTTP Boot of a RAM-disk ISO | Optional; QEMU/OVMF or real hardware |
| Proxy DHCP :4011 boot server path, `listenOn67: false` | Untested with real firmware (Hyper-V doesn't use 4011) |
| `boot_session=lease` on a real boot | Covered by `TestClientRoot`; hard to hit by hand |
| Content HTTPS, generated CA, `/settings/content-ca` | Phase 6 (decided 2026-11-06); until then the boot prompt password is sent in clear (ADR 0013 note) |
| Admin API authentication (`bearerAuth`) | Phase 6; admin binds to localhost by default |
| `ownerProcess` in bind failures | Always empty; Windows `GetExtendedUdpTable` / `GetExtendedTcpTable` would fill it |
| Hyper-V host build and media build numbers in the Phase 1 findings | Blank |

## Lab and environment

- Windows dev machine, Go 1.27.3 (pinned), PowerShell 7 + `just`.
- Hyper-V lab: internal switch `NetDeployLab`, host `10.77.0.1/24`; `nd-win`
  (the boot test VM, MAC `00:15:5d:00:85:09`), `nd-linux` (`10.77.0.2`, has
  dnsmasq installed but disabled).
- Running the server: [running-the-lab.md](../dev/running-the-lab.md).
  Settings are stored in the data directory, so use a fresh `-data` folder
  when switching between proxy and full mode experiments.
- Claude sessions in the cloud can't download Go modules or the toolchain;
  Phase 2 built Go 1.27.3 from the toolchain in the developer's module cache
  and staged the needed modules from it. Expect to do the same, or run tests
  on the Windows machine.

## Gotchas met in Phase 2

- `Tee-Object` overwrites: use `-Append` when a run has two parts.
- The tus and SSE endpoints don't fit the strict JSON handler model;
  `/events` uses a custom response type (`eventStream`) that streams from the
  `Visit…Response` method. tus will need the same or a plain handler.
- A long-lived request (SSE) holds up `http.Server.Shutdown`; admin requests
  use the server's lifetime as their base context so they end on shutdown.
- `MacAddress` accepts hyphens and any case; responses are always lower-case
  with colons.
- Firmware sends DISCOVER and then REQUEST within seconds; anything throttled
  per MAC must still record new information (the `lastIp` bug).

## Notes for Phase 4 (added during Phase 3)

- **Deployments and reboots** (2026-11-07): a client that reboots part-way
  through a deployment fails that deployment, unless the reboot is the
  expected one after a successful install. Phase 4's agent/deployment state
  machine must tell the two apart (e.g. the agent reports completion before
  rebooting, ADR 0013's BootNext handling).
- Server jobs (imports, builds) are different: any restart fails a running
  job with `interrupted` (ADR 0016).
