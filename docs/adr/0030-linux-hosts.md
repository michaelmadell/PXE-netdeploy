# 30. Linux hosts: a service account with two capabilities

Status: accepted (2026-11-19). Narrows Phase 6 decision 4 (iPXE and
non-Windows OSs move to a phase of their own); builds on
[0021](0021-secrets-storage.md) and [0022](0022-building-boot-images.md).

## Context
Phase 1 proved DHCP and TFTP from a Linux VM, and most platform code already
has a Linux side: the data lock, the 0600 secrets key, `SO_BINDTODEVICE` for
DHCP, wimlib as the only builder, `cabextract` for LZX CABs, the pure-Go SMB
client. Nobody had run the whole server there. Ports 67, 69, 80 and 443 and
raw ICMP need privileges; without them the server only showed bind failures
or a log line, the DHCP ping check turned itself off quietly, and
`ownerProcess` was always empty. ISO9660 import is about importing Linux
*media*, not about Linux hosts, so it goes with iPXE.

## Decision
- **Scope.** Phase 6 decision 4 is the Linux host. iPXE with the signed shim,
  ISO9660 media, Linux install flows and an OS type on images and profiles
  become a phase of their own.
- **A dedicated non-root account** with `CAP_NET_BIND_SERVICE` (ports below
  1024) and `CAP_NET_RAW` (raw ICMP for the ping check; `SO_BINDTODEVICE` on
  kernels before 5.7). Packaging (decision 5) writes the systemd unit and
  account; this decides what they grant. Root works but isn't recommended.
- **Privileges are reported, not discovered by failure.** At start and on each
  read the server works out what it may do: Linux from `CapEff` in
  `/proc/self/status` (or uid 0) and `net.ipv4.ip_unprivileged_port_start`;
  Windows from elevation. `Capabilities.privileges` (`lowPorts`, `icmp`,
  `missing`) and the `missing_privileges` health warning (only for privileges
  a configured service needs) say what is missing and how to grant it.
- **Ping check falls back**: a raw ICMP socket, else Linux's unprivileged ICMP
  datagram socket (`net.ipv4.ping_group_range`), else none, with the
  `dhcp_ping_unavailable` warning while full-mode DHCP has the check on.
- **`ownerProcess`** on bind failures: Linux from `/proc/net/{tcp,udp}` and
  `/proc/<pid>/fd` socket inodes (other accounts' processes need
  `CAP_SYS_PTRACE`, so best effort); Windows from
  `GetExtendedTcpTable`/`GetExtendedUdpTable` and the process image name.
- **Data directory checks** (Linux): the directory and `secrets.key`,
  `admin-key.pem`, `content-ca-key.enc`, `setup-code.txt` and `netdeploy.db`
  (with its `-wal` and `-shm` files) should be the server account's and not
  readable by group or others; `data_dir_permissions` warns otherwise. A
  warning, not a refusal to start, so a misconfigured server can still be
  fixed from the UI. The server sets its umask to `077` at start (added
  2026-11-19, when the lab showed SQLite creating the database `644`), so
  what it creates is private whoever starts it.
- **Verification**: CI on Ubuntu installs `wimtools` and `cabextract`, runs a
  wimlib test on a synthetic two-image WIM (export of the boot index, update,
  export) and a smoke test of the Linux binary. The lab runs the server in an
  Ubuntu VM as the service account and deploys `nd-client-01` from a boot
  image built there with the distribution's wimlib.

## Consequences
- A Linux install needs `wimtools` (and `cabextract` for LZX CABs) from the
  distribution; nothing else.
- Running as a normal user without the capabilities works for development
  (high ports, no ping check) and says so.
- `ownerProcess` may be empty for processes of other accounts on Linux.
