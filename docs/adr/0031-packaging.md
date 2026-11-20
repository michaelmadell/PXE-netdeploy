# 31. Packaging: an Inno Setup installer and a .deb

Status: accepted (2026-11-19). Phase 6 decision 5. Builds on
[0021](0021-secrets-storage.md), [0022](0022-building-boot-images.md),
[0029](0029-admin-authentication.md) and [0030](0030-linux-hosts.md).

## Context
Until now netdeployd ran from `go run`, `just dev`, or a binary copied by hand
with a systemd unit written from the lab guide. A Windows host needs a
service that survives reboots; a Linux host needs the account, unit and
capabilities ADR 0030 settled. Upgrades already work at the database level:
migrations run at start, a snapshot (`netdeploy.db.bak-v<N>-<time>`) is taken
first, and a database newer than the binary is refused. The generated admin
certificate lasts two years and was never renewed. wimlib's licence had to be
checked before bundling it (ADR 0022). netdeploy is a personal open-source
project, not a company product: releases are unsigned until an open-source
code-signing certificate is in place.

## Decision
- **Versions** come from git tags (`v0.6.0`) via `git describe`; between
  tags `0.6.0-3-gabc1234`. The `.deb` turns that into `0.6.0~dev3+gabc1234`
  (sorts before `0.6.0`), Inno Setup's numeric file version into `0.6.0.3`.
  Shown in health (`version`, signed-in callers only) and the web UI's
  sidebar.
- **Windows: an Inno Setup installer** (x64).
  - Program in `C:\Program Files\netdeploy`, data in
    `C:\ProgramData\netdeploy` (a page lets you choose another place: one
    Windows ISO needs about 20 GB).
  - netdeployd is a **native Windows service** (`golang.org/x/sys/windows/svc`):
    `netdeployd service install|uninstall|start|stop` registers it
    (automatic start, restart on failure, logs to `<data>\logs`), so the
    installer and scripted installs share one definition.
  - **LocalSystem**, so raw ICMP and the DISM fallback work as on an
    elevated shell. A virtual account was considered: less privilege, but
    neither of those.
  - The data directory gets a protected access list: SYSTEM,
    Administrators and the account that created it (the installing
    administrator), nothing inherited. The secrets key is protected with machine-scope
    DPAPI, so anyone who can read the file can unwrap it. The server also
    gives a data directory it creates itself that list (plus the account
    running it), and `data_dir_permissions` now checks Windows access lists
    too. The installer creates the directory through `admin set-address`,
    so both paths give the same list.
  - A firewall rule for `netdeployd.exe` (a checkbox, on by default): a
    program rule, because the ports come from settings.
  - **The admin address is asked at install time** ("this computer only"
    or "all networks", and the port) and written with the new offline
    command `netdeployd admin set-address`.
  - The finish page starts the service and shows the setup code and the
    address to open.
  - **wimlib is bundled** in `{app}\wimlib` with its licence files: the
    server looks there before `PATH`. wimlib-imagex is GPLv3+ and libwim
    LGPLv2.1+ (the release's `COPYING.txt`); netdeployd runs it as a
    separate, unmodified program, and the licences and the source's address
    ship with it.
  - Upgrades: same `AppId`; the service is stopped, files replaced, the
    service started. Uninstalling keeps the data unless you tick the box,
    and then the data directory is **moved aside**
    (`netdeploy.removed-<time>`), never deleted.
- **Ubuntu: a `.deb`** (amd64, arm64), built with nfpm.
  - `/usr/bin/netdeployd`, the unit in `/usr/lib/systemd/system`, the
    account from `systemd-sysusers`, `/var/lib/netdeploy` from the unit's
    `StateDirectory` (mode 700). Depends on `wimtools`, recommends
    `cabextract`.
  - The unit is the lab unit hardened: the two capabilities, `UMask=0077`,
    `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`.
    Extra flags go in `/etc/default/netdeploy` (a conffile, kept on upgrade).
  - **The admin address is a debconf question** (default "all networks":
    a headless server is rarely administered from itself), written with
    `admin set-address`.
  - Install enables and starts the service and says where the setup code is.
  - **Purge moves the data aside** to `/var/lib/netdeploy.purged-<time>`
    (a rename: instant, whatever its size) and says so. The `netdeploy`
    account stays, as Debian keeps system accounts, so the files keep a
    known owner. A reinstall starts fresh; moving it back restores everything,
    the secrets key included.
- **The generated admin certificate is renewed** at start when it expires
  within 30 days; the old pair is kept as `admin-cert.pem.old`. A custom
  certificate is left alone.
- **Versions in one place**: `scripts/version.ps1`, used by both
  `build.ps1` and `package.ps1`.
- **Building**: `just package` builds the installer (needs Inno Setup's
  `ISCC`) and the `.deb`s into `dist`. A `v*` tag runs a release workflow
  that attaches them to a GitHub release. CI installs the `.deb` on Ubuntu
  and runs the smoke test against the installed service. No apt repository
  yet. Signing: a hook (`NETDEPLOY_SIGN`) for later.

## Consequences
- Unsigned releases: SmartScreen warns on Windows until there is a
  code-signing certificate.
- A Windows data directory that inherits from `C:\` (Users can read) now
  warns `data_dir_permissions`; the message gives the `icacls` command.
- Purged data stays on disk until someone deletes it.
- Building the installer needs Windows (ISCC); the `.deb` builds anywhere Go
  runs. nfpm needs a newer Go than the repo's pin, so `package.ps1` sets
  `GOTOOLCHAIN=auto` for it.
- Licence (decided 2026-11-19): **Apache-2.0** (`LICENSE`, `NOTICE`); the
  installer shows it and installs it with the program, the `.deb` refers to
  the system's copy. That is also what open-source code-signing programmes
  ask for.
