# netdeploy

Network deployment for Windows 11 and Ubuntu over PXE and UEFI HTTP(S) Boot,
on machines with **standard Secure Boot**: Microsoft-signed boot managers and
shims (2011 and 2023 CA chains), no custom keys enrolled on the clients.

A WDS-style workflow in one server: import ISOs and driver packs, build
Windows boot images with the drivers your hardware needs, combine them into
profiles, and deploy registered machines remotely or from a password-protected
prompt at the machine. Runs on Windows or Linux as a single service with a web
UI and a documented REST API.

> **Status: pre-release (0.x).** Lab-tested on Hyper-V and an Intel NUC under
> Secure Boot. Expect rough edges, and read [Known limitations](#known-limitations)
> before using it on a network you care about. Binaries aren't code-signed yet.

## Features

- **Windows 11** over PXE (TFTP, Microsoft's boot manager directly) or UEFI
  HTTP(S) Boot (through Microsoft-signed iPXE). WinPE boot images built with
  wimlib or DISM, with storage and network drivers; a deployment agent that
  partitions, applies the image, adds OS drivers and sets the boot order.
- **Ubuntu Server** over UEFI HTTP(S) Boot, through the distribution's own
  signed shim: automatic installs (account, SSH keys, hostname pattern, disk
  layout, packages) from autoinstall templates, or the interactive installer.
- **Unattend and autoinstall templates** with live previews that never show
  secrets; local accounts, Active Directory join (online or offline), or none.
- **Built-in DHCP** (off, proxy alongside your DHCP server, or full), TFTP,
  and HTTP/HTTPS content with an internal CA.
- **Re-image protection:** machines only install when a deployment is armed
  for them, or when someone at the machine enters the boot prompt password.
- **Operations:** encrypted secrets, scheduled backups (folder or SMB share),
  admin accounts and API tokens, live deployment progress and logs.

## Install

Download the latest release from the
[Releases page](https://github.com/michaelmadell/PXE-netdeploy/releases):

- **Windows** (10/11 or Server 2019+): run `netdeploy-<version>-setup.exe`.
  It installs a service, asks where the admin UI listens, and shows a setup
  code on its last page. SmartScreen warns because it isn't signed.
- **Ubuntu 24.04** (amd64 or arm64):
  `sudo apt install ./netdeploy_<version>_amd64.deb`; it prints the setup code.

Then open the admin UI (`https://localhost:8443` by default), enter the setup
code and create the first account. The [user guide](docs/user/README.md)
walks through configuring the network and deploying the first machine.

## Documentation

- [User guide](docs/user/README.md): installing, setting up, deploying
  Windows and Ubuntu, troubleshooting.
- [Architecture](docs/architecture.md) and [decision records](docs/adr/README.md):
  how it works and why.
- [API](api/openapi.yaml): the OpenAPI 3.0 spec the server implements.
- [Building from source](docs/dev/getting-started.md).

## Known limitations

- **Ubuntu** deploys over HTTP(S) Boot only (PXE clients with a Linux profile
  get no answer), and only Ubuntu: Debian and RHEL ISOs import but can't be
  deployed yet. Ubuntu's current shim is signed by Microsoft's 2011 UEFI CA
  only, so machines that trust only the 2023 CAs refuse it.
- **Windows:** no Microsoft Entra/Autopilot join and no `setup.exe`/SMB
  install method yet.
- **Trusted network assumed:** the Ubuntu installer downloads its ISO,
  settings (with a password *hash*) and reports over plain HTTP, and the boot
  prompt password crosses the network in clear unless content HTTPS is on.
  Run deployments on a network you control. See [SECURITY.md](SECURITY.md).
- Untested so far: arm64 clients, Secure Boot databases with only the 2023
  CAs, proxy DHCP's port 4011 path on real firmware.

## Contributing

Bug reports and ideas are welcome as
[issues](https://github.com/michaelmadell/PXE-netdeploy/issues); pull requests
aren't being accepted yet. See [CONTRIBUTING.md](CONTRIBUTING.md). Report
security problems privately: [SECURITY.md](SECURITY.md).

## Licence

Apache License 2.0: see [LICENSE](LICENSE) and [NOTICE](NOTICE). The packages
also ship unmodified third-party binaries under their own licences: wimlib
(Windows installer) and the signed shim, iPXE and wimboot (both packages).
