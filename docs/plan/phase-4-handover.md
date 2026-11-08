# Phase 4 handover

Written 2026-11-08 at the end of Phase 3. Gives a new session what it needs
to design and build Phase 4 without the Phase 3 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 4 of netdeploy. Read `PROJECT_BRIEF.md` and
> `phase-4-handover.md` in the project first, then `phase-3-findings.md`,
> `phase-1-findings.md`, `architecture.md`, `adr-index.md` and the relevant
> parts of `openapi.yaml`. The repo is my connected folder `PXE-Netdeploy`.
> Follow "How we work" in the brief: propose the design for one area at a
> time, get my agreement, then change the spec, then implement. Start with
> decision 1 in the handover (building a boot image).

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work (no co-author trailers in commits; give `git add` lines with commit commands) |
| This file | Phase 4 starting point |
| `docs/spikes/phase-3-findings.md` | Media layout, boot managers, drivers, DISM, SMB and backup measurements |
| `docs/spikes/phase-1-findings.md` | Boot Manager, BCD, TFTP and Secure Boot facts the builder must reproduce |
| `docs/architecture.md` | Components, boot flows, deployment sequence, roadmap |
| `docs/adr/README.md` | Decisions 0001–0021; 0005 (builders), 0006 (BCD per boot image), 0007 (install methods), 0008/0021 (secrets), 0013 (boot prompt) matter most |
| `api/openapi.yaml` | `/boot-images`, `/profiles`, `/deployments`, `/agent/*`, `BootImage`, `Profile`, `Deployment`, `DeploymentStatus`, `DeploymentStage` |
| `docs/dev/running-the-lab.md` | Running the server, uploads, imports, backups |

## State at handover

Phases 1–3 are built and tested. Phase 3 added:

| Package | What it does |
|---|---|
| `internal/cas` | Content store (ADR 0015): blobs at `sha256/ab/<hash>`, trees as manifests served as `fs.FS` (case-insensitive), staging with atomic commit, mark-and-sweep (`tree_refs`/`blob_refs` views), space reserve, sweep hold for backups |
| `internal/jobs` | Jobs (ADR 0016): the `jobs` table is the queue, lanes `build` 1 / `import` 2 / `backup` 1, throttled progress on `/events`, cancel, `interrupted` on restart, Abandon before the final status |
| `internal/uploads` | Own tus 1.0.0 server (ADR 0017): hashed while receiving, 64 MiB checkpoints, 64 GiB limit, 24 h expiry unless imported, `Consume` for secrets |
| `internal/media/{udf,wim,signer}` | Own UDF and WIM XML readers, `go-wim` for files inside WIMs, Authenticode signer classification against the pinned Microsoft UEFI CAs (ADR 0018) |
| `internal/imports` | `image.import` and `driverPackage.import` jobs |
| `internal/drivers/{inf,catalog,archive}` | INF parser, catalog verification against pinned Microsoft roots, zip/7z/CAB extraction (LZX via `expand.exe`/`cabextract`) (ADR 0019) |
| `internal/secrets` | Data key (DPAPI/0600 file), AES-256-GCM values bound to id/kind/version, `Vault`, passphrase export/import (ADR 0021) |
| `internal/backup` | Scheduled/manual backups to a folder or SMB share, retention, restore (ADR 0020) |
| `internal/datalock` | OS lock on `<data>/netdeployd.lock` held by the server and offline commands |

Endpoints implemented: everything in Phase 2 plus `/uploads` (tus),
`/images`, `/driver-packages`, `/driver-groups`, `/jobs`, `/secrets`,
`/system/capabilities`, `/system/backups`. Still "not implemented yet":
`/boot-images`, `/unattend-templates`, `/profiles`, `/deployments`,
`/smb-targets`, `/tokens`, `/agent/*`.

Offline commands: `netdeployd key export|import`, `netdeployd backup`,
`netdeployd restore`.

Still temporary: `-tftp-root`, `-boot-file`, `-http-boot-url` (replace them
with boot images this phase), and `boot.Static` as the `Booter`.

## Things Phase 3 established that Phase 4 must respect

| Fact | Consequence for Phase 4 |
|---|---|
| The 2023-signed boot manager exists only inside `boot.wim` (both indexes); the media root has only the 2011 one | `bootmgr: auto` takes `bootmgfw_EX.efi` from the source image's boot.wim; import already records each boot manager's path, signer and SHA-256 (`details.bootManagers`) |
| Trees are served as `fs.FS` from manifests; `TreeBootImage` already exists as a tree kind | A built boot image is a tree (boot manager, BCD, boot.sdi, fonts, boot.wim) committed through staging; TFTP/HTTP serve it via `boot.Answer.Root` |
| `tree_refs`/`blob_refs` views decide what the sweep keeps | Migration adding `boot_images` must redefine both views to include them |
| DISM needs elevation even for `/?`; capabilities reads its version from the PE | Capabilities currently says `dism` is available unelevated: report "needs elevation" (and fall back to wimlib) when the server isn't elevated |
| Builds run on the `build` lane, one at a time; any restart fails them `interrupted` | A DISM mount left behind by a crash must be cleaned at startup (`dism /Cleanup-Mountpoints` or our own mount directory under staging) |
| Driver INFs carry `architectures`, `role` (storage = boot-critical) and `signature` | Validation: refuse `unsigned` drivers in boot images (Secure Boot), warn on `vendor`; the wimlib builder can't take storage drivers (ADR 0005) |
| Driver group membership or INF enable changes should mark dependent boot images `stale` (spec) | Needs the boot image → driver group links in SQL, and the 409s now unused: `deleteImage` `image_in_use`, `deleteDriverGroup` 409 |
| Secrets store only the current value (`value_version` increments) | Deployments snapshot secret *versions* (ADR 0008): keep old sealed values (a `secret_values` table keyed by version) or seal a copy into the deployment |
| Migrations can run with foreign keys off (`-- netdeploy: foreign_keys=off`) | Use it for the `clients` rebuild that adds the `profiles` foreign key (ADR 0014) |
| Settings hold a custom admin private key in the database in clear (`settings.admin.privateKeyPem`) | Consider moving it into secrets while touching settings; backups currently carry it (they carry the generated key file too) |
| `/smb-targets` now reference a `share-credential` secret | The SMB install method (ADR 0007) decrypts it with `Vault.Credential` |
| Admin API has no auth (localhost by default) | Agent endpoints are on the content listener (ADR 0009); agent auth is the check-in token, not admin auth |

## Decisions to make first

Phase 4 is the largest phase. Suggested order: 1 → 2 → 3 (server side
complete, a boot image boots per client), then 4 → 5 (agent and deployments).

### 1. Building a boot image

Inputs: a `windows` image and `sourceIndex` (boot.wim index, default 2),
driver groups, agent version, server URLs, builder (`auto`/`dism`/`wimlib`),
optional components, `bootmgr`. Settle:

- How each builder edits `boot.wim`: DISM mount/commit (elevated, Windows)
  vs `wimlib-imagex update` (unprivileged, both OSs; ship or require it?).
  Export only the chosen index into a new WIM so the image stays small.
- What goes in: the agent binary, `winpeshl.ini`, the server URLs and the
  content CA placeholder (Phase 6), staged drivers for wimlib (`drvload`).
- The output tree: boot manager (chosen per `bootmgr`), BCD, `boot.sdi`,
  fonts, `boot.wim`; stored through `cas` staging.
- Build caching by a hash of all inputs (ADR 0005) and `stale` marking.
- Where builds run (staging dir, mount dir), cancellation, and crash
  cleanup.

### 2. Writing the BCD

ADR 0006 says `bcdedit` on Windows and `hivex` on Linux. Options: those
tools (bcdedit needs elevation and a store file; hivex is a C library with a
Perl/CLI wrapper), or our own minimal REGF hive writer in Go (no tools, no
privilege, testable, but the format must be exactly right for Boot Manager).
Phase 1 recorded the BCD elements that worked (ramdisk options, TFTP block
and window sizes). Probably an ADR either way.

### 3. The real `Booter` (ADR 0013)

Replace `boot.Static`: per request, decide whether to answer and with which
boot image (client's profile, an armed deployment, `bootPrompt.defaultProfileId`
for unknown clients, `architectures` filters), and serve that boot image's
tree as the client's root. Remove the temporary flags. Decide caching (the
DHCP path is hot) and how `boot_session` maps TFTP/HTTP requests back to
the decision.

### 4. The agent (ADR 0003, 0007, 0013)

`cmd/agent` is a 70-line stub. Decide: the boot prompt UI in WinPE (console;
password check via `/agent/unlock`, countdown, profile menu), check-in and
token, partitioning (diskpart script vs Win32 IOCTLs), applying the image
(DISM in WinPE vs a bundled wimlib vs our own), injecting OS drivers,
placing unattend/identity files, progress and logs, BootNext and reboot, and
how the agent binary gets into a boot image (built per architecture, stored
as a blob).

### 5. Profiles and deployments

Profiles (Phase 4) and the deployment state machine. Settle: what a
deployment snapshots (profile, secret versions, boot image), the status and
`DeploymentStage` transitions, timeouts, and the reboot rule recorded below.
Unattend templates are Phase 5; Phase 4 needs the built-in template per
identity mode so a deployment can complete.

## Phase 4 scope

From the roadmap: boot image builder (DISM + wimlib), agent, profiles,
clients (exists), deployments. Phase 5 (identity, unattend templates, UI)
builds on these; keep the profile's identity and template fields in mind but
don't build Phase 5. Secrets storage, originally Phase 5, now exists
(ADR 0021).

## Carried over

| Item | Status |
|---|---|
| Phase 1 tasks 7–8: physical I210 device and timing runs | Waiting for hardware; sets final TFTP defaults and whether WinPE needs the I210 driver injected (decision 1 depends on it) |
| Phase 1 task 9: UEFI HTTP Boot of a RAM-disk ISO | Optional; QEMU/OVMF or real hardware |
| Proxy DHCP :4011 boot server path, `listenOn67: false` | Untested with real firmware |
| Content HTTPS, generated CA, `/settings/content-ca` | Phase 6; until then the boot prompt password is sent in clear (ADR 0013 note) |
| Admin API authentication (`bearerAuth`) | Phase 6; admin binds to localhost by default |
| `ownerProcess` in bind failures | Always empty |
| Hyper-V host build and media build numbers in the Phase 1 findings | Blank |
| Plain ISO9660 media (no UDF) | Refused until the Linux phase (ADR 0018) |
| `cloudsoda/go-smb2` | Pinned by commit (no tagged releases); NTLM tested, Kerberos not |
| Backups don't include `secrets.key` by design | Operators must run `netdeployd key export` once; health warns until they do |

## Lab and environment

- Windows dev machine, Go 1.27.3 (pinned), PowerShell 7 + `just`.
  The desktop shell Claude uses is not elevated: anything needing admin
  (DISM, bcdedit, creating shares) goes in a script the user runs elevated.
- Hyper-V lab: internal switch `NetDeployLab`, host `10.77.0.1/24`; `nd-win`
  (boot test VM, MAC `00:15:5d:00:85:09`), `nd-linux` (`10.77.0.2`, dnsmasq
  installed but disabled). Phase 4 is where the lab is needed again: a built
  boot image must boot `nd-win` per client.
- Win11 ISO used in Phase 3: `C:\Users\monke\Downloads\Windows-11.iso`
  (`CPBA_X64FRE_EN-GB_DV9`).
- Helpers in `.tmp` (git-ignored): `tus-smoke/smoke.mjs` (tus-js-client
  upload), `drv-e2e/run.ps1` (driver import end to end),
  `backup-e2e/*.ps1` (backup/restore/scheduler end to end),
  `smbspike/setup-share.ps1` and `cleanup-share.ps1` (elevated: a throwaway
  encrypted share).
- Tests run on Windows through PowerShell; file edits go through the Linux
  shell on the same folder. Opt-in real-data tests:
  `NETDEPLOY_TEST_ISO` (media, image import), `NETDEPLOY_TEST_DRIVERSTORE=1`
  (INF parser, catalogs, a driver package import).

## Gotchas met in Phase 3

- `fs.ReadDirFS` implementations must return names sorted.
- `smallstep/pkcs7` checks the signing time against certificate validity:
  test certificates must be valid when signing; it has no `SetContentType`.
- go-uefi's PKCS#7 verifier doesn't do ECDSA; sign test PE files with RSA.
- The kin-openapi validator reads whole bodies: anything streamed (tus,
  SSE) is routed before it.
- `oapi-codegen` can't tell `null` from absent for nullable fields; prefer a
  sentinel (e.g. `retention: 0` = keep all) over `nullable` with a default.
- `go get` of one module can bump shared ones (`x/text`) without their
  checksums; run `go mod tidy` before vetting.
- Polling for a job "created in the last N minutes" also matches the jobs
  you just made; track the ids you saw before.

## Notes for Phase 4 (from Phase 3)

- **Deployments and reboots** (2026-11-07): a client that reboots part-way
  through a deployment fails that deployment, unless the reboot is the
  expected one after a successful install. The agent/deployment state
  machine must tell the two apart (e.g. the agent reports completion before
  rebooting, ADR 0013's BootNext handling).
- Server jobs (imports, builds, backups) are different: any restart fails a
  running job with `interrupted` (ADR 0016); there is no resume.
- One build at a time is fine, with a robust queue behind it (user,
  2026-11-07): jobs already queue per lane in order.
