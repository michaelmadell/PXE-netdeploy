# Deploying Ubuntu

Ubuntu Server installs through UEFI **HTTP(S) Boot**, with Secure Boot on:
netdeploy's Microsoft-signed iPXE chain starts Ubuntu's own signed shim,
kernel and installer. There's no WinPE or agent; the installer reports its
progress itself.

Before you start:

- the deployment interface serves **HTTP** (Settings → Network), and **UEFI
  HTTP Boot** is on under Client types: the installer downloads its ISO and
  settings over plain HTTP;
- the client firmware boots from the network with **HTTP Boot** (on many
  machines a separate boot entry from PXE);
- Ubuntu's current shim is signed by Microsoft's **2011** UEFI CA only, so the
  client's Secure Boot database must still trust that CA (most do).

## 1. Import the ISO (Images)

Upload an Ubuntu Server live ISO (24.04 or later). It shows as **Linux**,
with the distribution, version and the signers of its shim; the ISO itself is
kept, because the installer downloads it. Debian and RHEL ISOs import too but
can't be deployed yet.

## 2. Make a Linux profile (Profiles → New profile → Linux)

Choose the Ubuntu image, then the install:

- **Automatic** (Ubuntu's autoinstall): the machine's largest disk is erased
  and installed with no questions:
  - an **account**: username, a password (a `password` secret) and/or SSH
    public keys; with keys only, password sign-in is locked and `sudo`
    doesn't ask for a password;
  - a **hostname pattern** (for example `lab-{{ .Client.Serial | last 6 }}`;
    by default `linux-` and the end of the MAC);
  - locale, keyboard layout, time zone, disk layout (LVM or plain
    partitions), extra packages;
  - after installing, start from **its disk** (the built-in template puts
    Ubuntu first in the boot order) or **the network** (netdeploy's
    countdown first, then the disk);
  - the **autoinstall template** (the built-in one, or your own copy).
- **Interactive**: the Ubuntu installer asks everything at the machine.

**Extra kernel arguments** are added to the installer's command line, for a
serial console for example. **Check** lists anything that would stop a
deployment.

## 3. Deploy (Clients)

As for Windows: **Deploy** a client and HTTP-boot it (a 10 s countdown, then
the installer), or at the machine press a key at netdeploy's countdown,
enter the boot prompt password and pick from the menu of Linux profiles.
Without either, the machine starts from its disk after the countdown.

An automatic install's deployment moves through starting the installer,
partitioning, writing the system, configuring and finishing, with the
installer's steps in its log, and ends **Installed** when the installer
finishes and the machine restarts. A failed install (a failing late command,
for example) marks it **Failed**; the installer's own logs are on the
machine in `/var/log/installer`. An interactive install is marked done as
soon as the installer starts, since nothing reports on it.

## Autoinstall templates (Unattend templates)

The built-in autoinstall template writes Ubuntu's `user-data` from the
profile. **New autoinstall template** copies it to customise (extra
`late-commands`, apt mirrors, snaps, and so on). Values from
`{{ … }}` are written as YAML (text quoted); the editor's reference lists
the fields, and the preview renders it with the password hash as
`********`. Keep the `reporting` section and the `curl` hooks, or netdeploy
can't follow the install (saving warns if they're missing).

The password reaches the installer only as a salted SHA-512 hash, over plain
HTTP; on a network you don't fully trust, prefer SSH keys.
