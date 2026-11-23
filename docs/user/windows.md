# Deploying Windows 11

The path: import an ISO, (optionally) add drivers, build a boot image, make a
profile, then deploy clients. The **Overview** page shows which step is
missing.

## 1. Import the Windows ISO (Images)

Upload a Windows 11 ISO. netdeploy reads its editions and the Microsoft-signed
boot managers it contains (2011 and 2023 CA). Uploads resume if interrupted.

## 2. Drivers (Driver packages, Driver groups), if needed

Upload vendor driver packs (zip, 7z or CAB). netdeploy reads their INF files
and checks their signatures (WHQL, vendor, unsigned). Put the packages you
need into **driver groups**:

- drivers WinPE needs to reach the network and the disk (NIC, storage such as
  Intel RST VMD or a RAID controller) go into the **boot image**;
- drivers the installed Windows needs go into the **profile** (OS drivers).

Many machines need neither: WinPE has inbox drivers for common hardware
(an Intel I210 NIC, for one).

## 3. Build a boot image (Boot images)

Create a boot image from the imported ISO: choose its driver groups and the
server addresses the agent should contact, then **Build**. netdeploy uses
wimlib (bundled on Windows, `wimtools` on Ubuntu) or DISM; a build takes
seconds to a minute. By default it uses the ISO's Setup image, which can add
OS drivers; plain WinPE (index 1) can't.

Rebuild when you change its drivers or after upgrading netdeploy: the boot
image page says when its inputs have changed since the last build.

## 4. Unattend template (Unattend templates), optional

Profiles use the **built-in** unattend template, which handles the computer
name, locale, product key and accounts. To add your own settings, copy it and
edit the copy; the preview shows the rendered file with every secret as
`********`, and saving checks it for each kind of accounts it handles.

## 5. Make a profile (Profiles)

A Windows profile combines:

- the boot image and the Windows image and **edition** to install;
- **accounts**: none (the user sets up Windows), local accounts (passwords
  from Secrets), or an **Active Directory** join, online with a join
  credential or offline with a per-client `djoin` blob;
- Windows settings: computer name pattern (for example
  `LAB-{{ .Client.Serial | last 6 }}`), locale, time zone, product key;
- OS driver groups, the unattend template, variables;
- the boot order after deploying: the installed disk first (the default) or
  the network first.

**Check** runs the same checks as deploying and lists anything that would
stop it. Make one profile the **default** for machines without their own.

## 6. Deploy (Clients)

Machines appear in **Clients** the first time they network boot, or add them
by MAC address. Give a client a profile, then **Deploy**: the next time it
network boots (PXE or HTTP(S) Boot) it shows a short countdown and installs.
Or, at the machine, press a key at the boot prompt, enter the password and
choose a profile.

The **Deployments** page shows each deployment's stage, progress and live
log. The agent partitions the disk, applies the image, adds OS drivers,
writes the boot files and sets the boot order; Windows Setup then finishes
on the machine (accounts, domain join). **Stop** cancels a running deployment
after the step in progress.
