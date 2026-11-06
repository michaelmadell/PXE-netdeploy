# Phase 2 handover

Written 2026-11-05 at the end of the Phase 1 session. Gives a new session
everything it needs to design and build Phase 2 without the Phase 1 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 2 of netdeploy. Read `PROJECT_BRIEF.md` and
> `phase-2-handover.md` in the project first, then `phase-1-findings.md`,
> `architecture.md`, `adr-index.md` and the relevant parts of `openapi.yaml`.
> The repo is my connected folder `PXE-Netdeploy`. Follow "How we work" in the
> brief: propose the design for one area at a time, get my agreement, then
> change the spec, then implement. Start with decision 1 in the handover
> (unknown-client policy).

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work |
| This file | Phase 2 starting point |
| `docs/spikes/phase-1-findings.md` | Measured behaviour of Boot Manager, firmware, DHCP, TFTP |
| `docs/architecture.md` | Components, boot flows, roadmap |
| `docs/adr/README.md` | Decisions 0001–0012 |
| `api/openapi.yaml` | `Settings`, `InterfaceSettings`, `BootImage.tftp`, `Client`, `DhcpLease`, `/settings`, `/dhcp/leases`, `/system/health` |
| `docs/spikes/phase-1-runbook.md` | Lab commands, if you need to re-test |

## State at handover

- Branch `main`. Phase 1 code and results are committed; the task 10 docs
  and this handover are committed together with it (see the commit at the end
  of the Phase 1 session).
- `internal/dhcp`: full mode only, one interface, in-memory leases, every
  client gets an address, UEFI x64 PXE gets the boot file, HTTP Boot gets a URL
  if configured. No proxy mode, no persistence, no client policy.
- `internal/tftp`: our own read-only server (ADR 0012): blksize, tsize,
  timeout, windowsize, rollover, `os.Root` sandbox, single root directory.
  Default caps 8192/8.
- `cmd/netdeployd`: services enabled by `-dhcp-*` / `-tftp-*` flags; health
  reports them. No settings store yet: everything is flags.
- Tests: unit tests for DHCP decisions, loopback tests for TFTP, all passing
  with `-race`. `just check-drift lint test build` clean on Windows; CI on
  Ubuntu and Windows.

## Phase 1 results that constrain Phase 2

All tested 2026-11-05 unless marked. Details and evidence in the findings.

| Finding | Consequence for Phase 2 |
|---|---|
| Boot Manager asks for `\BCD`, then `\Boot\BCD` | Per-client BCD (ADR 0006) is served at `\Boot\BCD`; nothing may exist at `\BCD` |
| Boot Manager reuses one source port across requests; DHCP gives the client IP | Map client IP to MAC from the DHCP exchange (or lease table) to choose files per client |
| Boot Manager re-downloads its own image as `\<bootfile>` (leading `\`) | The boot file must resolve both with and without a leading separator |
| ~20 optional files per boot are absent (policy `.p7b` files, `boot.stl`, `UnlockToken.pol`) | Log known-optional misses at debug so real misses stand out |
| Font wanted at `\EFI\Microsoft\Boot\Fonts\wgl4_boot.ttf` | Boot image layout must include it there |
| Boot Manager stops at `tsize` and never ACKs an empty final block | Fixed in `internal/tftp`; keep the test |
| boot.sdi and boot.wim download in parallel | Per-transfer sockets are fine; don't serialise per client |
| 16384/16 failed ("Windows failed to start"); 8192/8 never lost a packet on Hyper-V | Server caps must protect clients; defaults 8192/8 provisional until I210 hardware runs |
| Lost blocks cost a full timeout; Boot Manager doesn't ACK early | Consider shrinking the window after loss |
| Firmware PXE (Hyper-V) negotiates `windowsize=4`, `blksize=1482` | Firmware is a TFTP client too; same server rules apply |
| Media `bootx64.efi` is 2011-signed; `Windows\Boot\EFI_EX\bootmgfw_EX.efi` in boot.wim is 2023-signed | Image import must look inside the WIM; `bootmgr: auto` is viable |
| `Get-AuthenticodeSignature` reports catalog signatures | Signer detection must read the embedded signature |
| Hyper-V `MicrosoftWindows` template trusts the 2023 CA; `MicrosoftUEFICertificateAuthority` has no Windows CA | Lab facts; physical hardware tests the both-CAs case |
| Media Setup (`boot.wim` index 2) is blocked when network-booted (WDS deprecation) | Deployments always use our custom WinPE (index 1 base) + agent, or SMB setup.exe launched from it |
| Windows: DHCP socket on `<iface IP>:67` receives broadcasts. Linux: `0.0.0.0:67` + `SO_BINDTODEVICE` works | Keep both strategies; Default Switch ICS holds `172.x:67` and doesn't conflict |
| Hyper-V DHCP Guard drops a VM's DHCP offers | Note for docs when the server runs in a VM |
| Only **full** DHCP mode was tested | **Proxy mode is entirely untested** (and is the spec's default) |

## Decisions to make first

### 1. Unknown-client policy (supersedes ADR 0010)

**Decided 2026-11-05:** [ADR 0013](../adr/0013-boot-prompt-password-gate.md)
(every PXE client boots WinPE; password gate). Spec updated. The notes below
are the original brief.

**New requirement from the developer:** unknown clients are the *main*
clients that will boot. Known clients also boot when a deployment is being
re-tested.

ADR 0010 and `Settings.unknownClients.policy` (default `ignore`) assume the
opposite. Write a new ADR that supersedes 0010, then change the spec. Points
the design must settle:

- **Default policy.** Likely `default-profile` (or a new value such as
  "boot the default boot image and let the agent check in") instead of
  `ignore`.
- **No re-image loops.** Firmware often keeps network boot first. After a
  deployment completes, the client must become *known with no pending
  deployment* and fall through to its disk on the next boot, until someone
  schedules it again ("re-test"). Decide where that state lives (client record
  created at first boot, deployment status) and what the DHCP/TFTP layer
  checks.
- **Scope of "unknown".** On a dedicated deployment network answering every
  PXE client is fine; on a shared one it is dangerous. Options: per-interface
  policy, an arch/vendor-class filter, a MAC or subnet allow-list, or a
  confirmation step in the UI. Decide the safe default and how it's surfaced.
- **What "known" means.** Registered MAC (and/or SMBIOS UUID from option 97)
  in `/clients`. Check `Client.source` (`manual`, `registered`) still fits.
- **Re-test flow.** How a completed client is re-armed (schedule a new
  deployment via the API/UI) and whether it boots the same boot image.

### 2. Spec values that Phase 1 contradicts

**Decided 2026-11-05:** server caps 8192/8, timeout 1 s; boot images request
16384/16 and the server caps decide. Spec, findings ("Chosen defaults") and
the architecture roadmap updated. The table below is the original brief.

| Spec location | Current | Phase 1 says |
|---|---|---|
| `Settings.tftp.maxBlockSize` default | 16384 | 8192 (provisional) |
| `Settings.tftp.maxWindowSize` default | 64 | 8 (provisional) |
| `Settings.tftp.timeoutSeconds` default | 5 | Code uses 1 s; Boot Manager behaved well with 1 s. Pick one and justify |
| `BootImage.tftp.blockSize` maximum | 16384 | 16384/16 failed on Hyper-V; maybe cap lower or warn |
| ~~`Settings.unknownClients.policy` default~~ | ~~`ignore`~~ | Done: replaced by `Settings.bootPrompt` (ADR 0013) |
| `architecture.md` roadmap, Phase 1 row | "from our proxyDHCP" | Spike used full mode; proxy untested |

### 3. Storage

**Decided 2026-11-06:** [ADR 0014](../adr/0014-sqlite-store.md). Phase 2
slice: settings, clients, deployments, DHCP leases, idempotency keys;
`internal/store`. The notes below are the original brief.

Phase 2 needs persisted settings and leases (`full` mode: "persisted
leases"). ADR 0003 names SQLite via `modernc.org/sqlite` (no cgo); there's no
schema yet. Decide the Phase 2 slice: settings, leases, clients (needed by
decision 1), and migrations approach.

## Phase 2 scope

From the roadmap: core server with DHCP (proxy and full), TFTP, content HTTP,
settings and health. Suggested order, each designed then built:

1. **Decision 1 + client model** (ADR, spec, store): needed by DHCP and TFTP.
2. **Settings store and atomic apply** (**done 2026-11-06**: `internal/settings`,
   `internal/netsvc`, request validator; see running-the-lab.md) (`GET/PUT /settings`, ETag, 422 with
   per-field errors, bind-before-release as the spec describes). Replaces the
   spike flags; keep flags only for bootstrap (data dir, admin address).
3. **DHCP** (**done 2026-11-06**: proxy mode, stored leases, reservations,
   pingCheck, `/dhcp/leases`, ADR 0013 filters and sightings behind
   `boot.Booter`; proxy mode booted WinPE on Hyper-V, see
   spikes/phase-2-findings.md): per-interface modes `off` / `proxy` / `full`; proxy on 67 and
   4011 (`listenOn67`); persisted leases with `pingCheck`; reservations;
   `GET /dhcp/leases`; architecture filters from `Settings.architectures`;
   `externalDhcpGuidance` for `off`; policy hook from decision 1.
4. **TFTP** (**done 2026-11-06**: per-client content via `tftp.Config.ClientRoot`
   from boot sessions, else the lease table, else the default; `\BCD` never
   served; optional misses at debug; adaptive window not done, see
   phase-2-findings.md. Also `/clients` CRUD and Idempotency-Key): virtual tree (per-client BCD at `\Boot\BCD` via IP to MAC
   mapping; boot image files from the store); quiet logging for known-optional
   files; settings-driven caps; optional adaptive window.
5. **Content HTTP** (**done 2026-11-06**, HTTP only: `internal/content` serves
   `/content/boot/` per client and the agent API, nothing else; HTTPS moved to
   Phase 6 by decision, `services.https: true` is refused until then) listener
   per interface (ADR 0009), serving boot files and WIMs; HTTPS optional.
6. **Health and events** (**done 2026-11-06**: `store` health check,
   `/events` SSE with `client.updated`, `lease.updated`, `settings.updated`,
   `health.updated`): real per-service status, `/events` if cheap.

**Phase 2 is complete.** Next: [phase-3-handover.md](phase-3-handover.md).

Phase 3 (imports, uploads) and Phase 4 (boot image builder, agent,
deployments) depend on this; keep their interfaces in mind but don't build them.

## Carried over from Phase 1

| Item | Status |
|---|---|
| Tasks 7–8: physical I210 device (stock WinPE, then driver injected) and the I210 timing runs (1456/8, 1456/16, 1456/32, 8192/8) | Waiting for hardware. Runbook ready. Sets final TFTP defaults |
| Task 9: UEFI HTTP Boot of a RAM-disk ISO | Optional, not done. Needs QEMU/OVMF or real hardware |
| Hyper-V host build number in the findings | Blank |
| Media build number in the findings | Blank |

## Lab and environment

- Windows dev machine; Go pinned to 1.27.3 (embedded spec differs across Go
  versions and breaks the drift check). PowerShell 7 + `just`.
- Module path `github.com/michaelmadell/pxe-netdeploy` (lowercase; matches
  the GitHub remote).
- Hyper-V lab: internal switch `NetDeployLab`, host `10.77.0.1/24`.
  `nd-win` (`MicrosoftWindows` template, boots), `nd-shim` (third-party CA
  only, refuses Windows), `nd-linux` (Ubuntu, `eth1` = `10.77.0.2/24`, for
  Linux-host tests).
- Spike boot files in `.tmp\spike` (gitignored): `bootmgfw.efi` (2011),
  `bootmgfw_EX.efi` (2023), `Boot\BCD` (ramdisk 16384/16), `Boot\boot.sdi`,
  `Boot\boot.wim` (plain WinPE, index 1 exported, 581.7 MB),
  `Boot\boot-setup.wim` (original), `EFI\Microsoft\Boot\Fonts\`.
- Known-good run (Phase 1, flags since removed):
  `go run .\cmd\netdeployd\ -dhcp-iface 'vEthernet (NetDeployLab)' -dhcp-pool 10.77.0.100-10.77.0.199 -tftp-root .tmp\spike -tftp-addr 10.77.0.1:69 -boot-file bootmgfw_EX.efi`.
  Since Phase 2 step 2: [running-the-lab.md](../dev/running-the-lab.md).

## Gotchas met in Phase 1

- Editing `.ps1` files from a script can drop CRLF; `.gitattributes` keeps
  `.ps1` as CRLF and everything else LF.
- `bcdedit /createstore` writes into the current directory; run it inside
  `.tmp\spike\Boot`.
- `Get-AuthenticodeSignature` shows catalog signatures; use
  `X509Certificate.CreateFromSignedFile` for the embedded signer.
- Don't stop `SharedAccess` for port 67 unless it's bound to `0.0.0.0` or the
  lab address.
- The Phase 1 spike code is intentionally minimal; Phase 2 replaces its flags
  and in-memory state rather than extending them.
