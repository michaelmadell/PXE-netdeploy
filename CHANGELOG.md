# Changelog

Notable changes to netdeploy. Versions follow [semantic versioning](https://semver.org/);
before 1.0, minor versions may change behaviour.

## Unreleased: 0.1.0 (first release)

### Windows 11
- PXE boot with Microsoft's boot manager directly (2011 or 2023 CA), and UEFI
  HTTP(S) Boot through the Microsoft-signed iPXE shim, iPXE and wimboot.
- ISO import with editions and signed boot managers; driver packages (zip,
  7z, CAB) with INF parsing and signature checks; driver groups.
- WinPE boot images built with wimlib or DISM, with boot drivers and the
  netdeploy agent.
- The agent: disk selection, partitioning, image apply, OS drivers, WinRE,
  boot files and the UEFI boot order, with live progress and logs.
- Unattend templates with masked previews; local accounts, Active Directory
  join (online or offline), computer name patterns.

### Ubuntu
- Ubuntu Server over UEFI HTTP(S) Boot under Secure Boot, through Ubuntu's
  own shim.
- Linux ISO import (ISO 9660 with Rock Ridge and Joliet); Ubuntu, Debian and
  RHEL media recognised, Ubuntu deployable.
- Automatic installs from autoinstall templates (account, SSH keys, hostname
  pattern, disk layout, packages, boot order), or the interactive installer.
- Progress and the result reported by the installer.

### Server
- DHCP (off, proxy, full), TFTP with windowsize, HTTP and HTTPS content with
  an internal CA.
- Re-image protection: armed deployments, or the boot prompt password (in
  WinPE for Windows, in iPXE for Linux).
- Admin accounts and API tokens, encrypted secrets, scheduled backups to a
  folder or SMB share.
- Windows installer and Ubuntu packages (amd64, arm64).

### Known limitations
See the [README](README.md#known-limitations).
