# 25. The WinPE agent and its API

Status: accepted (2026-11-08). Builds on [0003](0003-go-for-server-and-agent.md),
[0007](0007-agent-and-smb-install-methods.md) (agent install method),
[0008](0008-secrets-model.md) and [0013](0013-boot-prompt-password-gate.md).

## Context
ADR 0022 put the agent into every boot image, launched alone by
`winpeshl.ini`. It now has to run a deployment: the boot prompt, check-in,
partitioning, applying the image, OS drivers, boot files, boot order and
reporting. Arming deployments through the admin API (`POST /deployments`),
timeouts and the built-in unattend are decision 5; the SMB/`setup.exe`
method and per-edition image exports come later in Phase 4.

## Decision
**Server (`/agent/*`, on the content listener, ADR 0009):**
- *Check-in* creates or updates the client and answers `armed` (a
  `pending` deployment, from the IP DHCP gave that MAC when this server saw
  the exchange; it moves to `running` and a deployment token is issued) or
  `locked`. **A client that checks in while its deployment is `running`
  restarted mid-deployment: that deployment is marked `failed`
  (`restarted`).** A successful deployment reports `complete` before it
  reboots, so its reboot never counts.
- *Unlock* checks the boot prompt password (argon2id) and returns a
  single-use session token (5 minutes). Failures are rate-limited in memory
  per MAC and per source IP: 5 free, then a doubling back-off up to
  5 minutes (`429`, `Retry-After`).
- *Start from the prompt* snapshots the profile and starts a `running`
  deployment (superseding any other active one), returning a deployment
  token valid until `complete` or 24 h.
- Tokens are random 32 bytes, stored as SHA-256 hashes.
- *Plan, progress (stages only move forward), logs, complete* as the spec
  says. Logs are stored per deployment.
- **`GET /agent/files/{sha256}`** serves, with byte ranges, only the files
  the deployment's plan lists (install image, OS driver files), to a valid
  deployment token.

**Agent (WinPE console):**
- Screens: armed countdown (any key: menu); locked password prompt with
  countdown (debug-mode banner; "only armed deployments" without a
  password); profile menu with the client's or default profile
  preselected; a confirm screen naming the target disk; progress with the
  last log lines; on failure, report it and stay on the error until a key
  or 10 minutes, then reboot (to the prompt, then the disk: no loop).
- **Target disk** `null` = auto: ignore USB, SD and MMC disks and disks
  under 32 GB; take the lowest-numbered of the rest. An explicit index wins.
- **Partitioning** with diskpart, Microsoft's UEFI/GPT layout: EFI 260 MB
  FAT32 (S:), MSR 16 MB, Windows NTFS (W:), recovery 1 GB at the end (R:),
  WinRE registered with `reagentc /setreimage … /target`.
- **Image** downloaded to `W:\netdeploy\` (SHA-256 checked, resumable),
  applied with `dism /Apply-Image`, then deleted: DISM can't read HTTP and
  WinPE's RAM disk can't hold the image. The whole `install.wim` for now.
- **OS drivers**: every file of each enabled INF's folder from the
  profile's `osDriverGroupIds`, added with `dism /Image:W:\ /Add-Driver
  /Recurse` (never `/ForceUnsigned`). Storage drivers (PERC, VMD) belong in
  both the boot image's groups (WinPE sees the disk) and the profile's.
- **Boot files**: `bcdboot W:\Windows /s S: /f UEFI`, plus `/bootex` when
  the boot image serves the 2023-signed boot manager.
- **Unattend** (when the plan has one): `W:\Windows\Panther\unattend.xml`
  and a `SetupComplete.cmd` that deletes it.
- **Boot order** through UEFI variables from Go
  (`Get/SetFirmwareEnvironmentVariableEx`, `SeSystemEnvironmentPrivilege`),
  not `bcdedit`, which may not see firmware entries from a RAM-disk WinPE:
  after a deployment `disk-first` (bcdboot's order) or `network-first`
  (the network entry moved back to the top); on a prompt timeout `BootNext`
  = the local disk's Windows Boot Manager, or power off if there is none.
  Completion is reported before the boot order changes.
- Logs: batched to the server every 5 s and kept in
  `X:\netdeploy\agent.log`, copied to `W:\Windows\Temp` at the end.
  Network calls retry with back-off; a failed progress report never stops
  a deployment.
- Code in `internal/agent/…`; commands run through an interface so tests
  check the diskpart scripts and command lines; the generated API models
  with a small hand-written client.

## Consequences
- A deployment can run end to end from the boot prompt before decision 5.
- Each deployment downloads the whole `install.wim` (8 GB for ten
  editions); a per-edition export would roughly halve it.
- To verify in the lab: `bcdboot /bootex` in WinPE; UEFI variables from
  WinPE; the disk auto-selection on real machines.
