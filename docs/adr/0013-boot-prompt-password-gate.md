# 13. Every PXE client boots WinPE; a password gate prevents re-imaging

Status: accepted. Supersedes [0010](0010-unknown-clients-ignored-by-default.md).

## Context
ADR 0010 ignored unknown PXE clients by default. In practice unknown machines
are the main clients, and known machines must also be re-imaged easily when a
deployment is re-tested. Either way, firmware often keeps network boot first,
so whatever answers a machine must not re-image it on every boot (including
the 2–3 reboots Windows Setup makes after the image is applied).

`bootmgfw.efi` can't prompt for anything, and an extra pre-WinPE EFI app would
need signing, which [ADR 0002](0002-separate-windows-boot-path.md) avoids. So
any interactive gate has to run in WinPE, in our agent.

## Decision
- **Who boots.** DHCP answers every PXE/HTTP Boot client on a configured
  interface that passes `Settings.architectures` and the interface's optional
  `macPrefixes` allow-list, provided there is a profile to offer (an armed
  deployment, the client's profile, or `bootPrompt.defaultProfileId`). A fresh
  install has no interfaces and no profiles, so it answers nobody.
- **Client records** are created at first contact (`source: discovered`): at
  DHCP time, or at agent check-in when DHCP is `off`. Clients are matched by
  MAC only; SMBIOS UUID is recorded, not matched. The `approved` flag and the
  `ignore` / `register` / `default-profile` policies are removed.
- **Armed deployments** (`POST /deployments`, status `pending`) run without a
  password: the agent shows a countdown (default 10 s) and any key opens the
  menu instead. Check-in consumes the deployment (`pending` → `running`).
- **Not armed:** the agent shows a password prompt with a countdown (default
  30 s). At zero it sets UEFI `BootNext` to the local disk and reboots. The
  correct password opens a profile menu with the client's profile (or the
  default) preselected; confirming starts a deployment in the WinPE that is
  already running, even if the profile names a different boot image.
- **One global password** in `Settings.bootPrompt`, stored as an argon2id hash
  and checked server-side (`/agent/unlock`), with failed attempts rate-limited
  per MAC and IP.
- **Debug mode** (`requirePassword: false`) skips the password but still needs
  someone to confirm a profile. It raises a health warning, a UI banner, a
  startup log warning, and a banner on the WinPE screen.
- **Boot order after deployment** is a profile setting,
  `bootOrderAfterDeploy: disk-first | network-first`, default `disk-first`
  (bcdboot's behaviour). The agent reports completion *before* changing boot
  order and rebooting.

## Consequences
- No re-image loops by construction: only `pending` deployments are armed, and
  they are consumed at check-in. A crash or reboot mid-deployment lands at the
  prompt, then the disk. Starting again from the prompt marks the old
  `running` deployment `failed` (`superseded`).
- Re-testing needs no config changes: arm it from the UI/API, or enter the
  password at the machine.
- Agent check-in no longer hands a deployment to anyone who spoofs a MAC,
  unless that MAC is armed.
- Every un-armed network boot downloads the full boot.wim before the prompt
  appears (~580 MB in the spike). With `network-first`, normal boots pay that
  plus the countdown. To measure on I210 hardware.
- The password crosses the deployment network over plain HTTP unless content
  HTTPS is enabled, which needs the internal CA in the boot image (Phase 4
  builder). Documented; HTTPS is recommended. (2026-11-06: content HTTPS was
  moved to Phase 6, so until then the password is sent in clear. Use a
  dedicated deployment network, and treat the password as protecting against
  accidents, not an attacker on that network.)
- On a shared network, anyone at a PXE-first machine can see the prompt; the
  password is the only gate. `macPrefixes` limits who is answered at all.
- To verify in a spike: setting `BootNext` / `BootOrder` from WinPE (Go,
  firmware variables or `bcdedit /set {fwbootmgr}`) and reliably finding the
  disk's Windows Boot Manager entry.
- Deploying from the booted WinPE with another profile's install image relies
  on the booted WinPE having the storage drivers that profile needs.
