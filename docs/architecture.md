# Architecture

## Goals

- Deploy Windows 11 over the network on stock Secure Boot hardware, including
  machines that trust only the Microsoft Windows CA (Secured-core PCs).
- WDS-style management: images, driver packs, boot images, profiles, clients.
- Runs on Windows and Linux hosts as one binary with a web UI and API.
- Other operating systems (Linux via distro shims, iPXE menus) in later phases.

## Components

```
                 ┌──────────────────────────── netdeployd ───────────────────────────┐
 admin network → │ Admin listener (HTTPS)   API /api/v1 + embedded UI                 │
                 │                                                                     │
 deploy network →│ DHCP (off / proxy / full)   TFTP (read-only virtual tree)           │
                 │ Content HTTP(S): boot files, WIMs, drivers, agent API               │
                 │                                                                     │
                 │ Jobs: ISO import · driver import · boot image builds (DISM/wimlib) │
                 │ Store: SQLite metadata + content-addressed files (SHA-256)          │
                 └─────────────────────────────────────────────────────────────────────┘
                                              ▲
                     WinPE agent (Go) ────────┘  check-in, plan, progress, logs
```

The admin and content listeners are separate so machines on the deployment
network can reach boot content but not the admin API ([ADR 0009](adr/0009-separate-admin-and-content-listeners.md)).

Imported and built files live in a content-addressed store under
`<data>/content` (`-content` overrides): each file once, named by its SHA-256,
with trees (an extracted ISO, a driver package, a boot image) described in
SQLite and served to TFTP and HTTP as a virtual filesystem
([ADR 0015](adr/0015-content-addressed-store.md)).

## Windows boot flows

**PXE (TFTP).** Firmware gets a boot file from DHCP (our proxy or full mode, or an
external server configured per `externalDhcpGuidance`) and downloads the
Microsoft-signed `bootmgfw.efi`. The boot manager requests `\Boot\BCD`; the TFTP
server maps the client's IP to its MAC (from the DHCP exchange) and serves the BCD
built for that client's deployment. The BCD points at the profile's `boot.wim`,
which loads as a RAM disk. No shim is involved, so this works on machines that
don't trust the third-party CA ([ADR 0002](adr/0002-separate-windows-boot-path.md)).

**UEFI HTTP Boot.** The Windows boot manager fetches its BCD and WIM through the
PXE/TFTP path, not HTTP. For HTTP Boot clients, a boot image can also be built as
a small ISO (boot manager + BCD + boot.wim) that the firmware downloads and mounts
as a RAM disk. Firmware support varies; this is on the Phase 1 spike list.

**Secure Boot certificates.** Windows media contains a boot manager signed under
the 2011 PCA and, in recent builds, a 2023-signed variant inside the WIMs
(`EFI_EX`). Image import records every boot manager it finds; a boot image serves
the 2023-signed one when available (`bootmgr: auto`), so the same media boots
machines that have revoked the 2011 chain.

## Deployment sequence

1. Admin schedules a deployment for a client (profile and secret versions are snapshotted).
2. Client network-boots; DHCP/TFTP serve its boot image and BCD.
3. WinPE starts the agent via `winpeshl.ini`: `wpeinit`, load staged drivers
   (wimlib builds), initialise networking, check in.
4. Agent fetches its plan, partitions, applies the install image (agent mode) or
   runs `setup.exe` from the SMB share (smb mode), injects OS drivers, places the
   unattend and identity files, reports progress, completes, reboots.
5. Setup completes; a SetupComplete step deletes the unattend file.

## Roadmap

| Phase | Scope |
|---|---|
| 0 | Spec, repo skeleton, tooling (this) |
| 1 | **Spike:** Win11 WinPE boots under Secure Boot on Hyper-V from our full-mode DHCP + TFTP (proxy mode untested); test HTTP Boot ISO |
| 2 | Core server: DHCP (proxy/full), TFTP, content HTTP, settings, health (**done 2026-11-06**) |
| 3 | Storage and imports: ISOs, driver packs, jobs, uploads (tus); backups and secrets storage (**done 2026-11-08**) |
| 4 | Boot image builder (DISM + wimlib), agent, profiles, clients, deployments |
| 5 | Identity, unattend templates; UI for all of the above (secrets storage moved to Phase 3) |
| 6 | Linux and iPXE paths, content HTTPS (generated CA, `/settings/content-ca`) and HTTPS boot, auth hardening, packaging |
