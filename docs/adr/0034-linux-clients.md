# 34. What a Linux client boots: Linux profiles, per-client iPXE scripts, the gate in iPXE

Status: accepted (2026-11-23). Phase 7 decision 3. Amends
[0013](0013-boot-prompt-password-gate.md) (the gate, for Linux profiles),
[0024](0024-booter.md) (what a client boots) and
[0032](0032-https-boot-ipxe.md) (the iPXE script becomes per client).
Builds on [0033](0033-reading-linux-media.md) and the Phase 7 spike
([findings](../spikes/phase-7-findings.md)).

## Context
The spike proved the boot path: the bundled iPXE chain (now `snponly.efi`,
which leaves the keyboard working), iPXE's `shim` command with the
distribution's own shim, then the kernel and initrd with our command line;
casper then downloads the whole ISO over **plain HTTP** (no TLS client in its
initramfs). The installer's kernel does its own DHCP and can get a
**different address** from iPXE (`.103` against `.102` on the NUC), so
anything it fetches can't be tied to the client by address. After an install
the machine may still network-boot first. Import (ADR 0033) records each
Linux image's shim, kernel and initrd and keeps Ubuntu's ISO.

Windows keeps its gate in WinPE (ADR 0013): a Linux client never gets there.

## Decision
- **Profiles have a `kind`**, `windows` (default, as before) or `linux`. A
  Linux profile has a name, a description, `installImageId` (a ready,
  deployable Linux image) and optional `kernelArgs`; the Windows fields are
  refused on it (`not_for_kind`) and required on Windows ones. `kind` can't
  change on replace. Stored in one `profiles` table (the Windows columns
  become nullable, with a check per kind).
- **What a client boots** is decided as before (armed deployment, the
  client's profile, the default; no fallback past a broken choice). For a
  Linux profile:
  - **HTTP(S) Boot clients** get the iPXE chain, as Windows ones do.
  - **PXE clients** get no answer, with the reason in the log ("Linux
    profiles need HTTP(S) Boot"); PXE for Linux is decision 6.
- **`autoexec.ipxe` is generated per client** at request time, from the same
  decision (session, lease, default profile): Windows profiles get the ADR
  0032 script unchanged; Linux profiles get the gate below.
- **Linux files are served by image**, at `/content/linux/<imageId>/` on the
  content listeners: the image's tree, and `image.iso` from its kept ISO;
  only for ready Linux images. Every URL in a script is absolute, so nothing
  depends on the client's address.
- **The kernel command line** (Ubuntu):
  `ip=dhcp url=http://<interface>:<httpPort>/content/linux/<id>/image.iso cloud-config-url=/dev/null`
  then the profile's `kernelArgs`. The ISO URL is plain HTTP on the interface
  the request came in on; the shim, kernel and initrd come over the
  request's own scheme (HTTPS when on). An interface without an HTTP
  listener can't serve Linux clients: arming and validation say so.
- **The gate, in the generated script** (amends ADR 0013 for Linux
  profiles):
  - *Armed deployment*: a 10 s countdown ("press any key for the menu"),
    then `POST /agent/ipxe/start` with `armed=true`: the deployment becomes
    `running` and the answer is the boot commands. A key goes to the
    password prompt instead.
  - *Not armed*: "press any key within 30 s to deploy"; with no key the
    script `exit`s to the firmware, which boots the next device (the disk).
    iPXE's `login` has no timeout, hence the countdown first.
  - A key: `login` (only its password field is used), posted to
    `/agent/ipxe/unlock`, checked server-side with the global boot prompt
    password (argon2id), rate-limited per MAC and IP as `/agent/unlock` is.
    The answer is a menu of the deployable **Linux profiles only**, the
    client's own (else the default) preselected, carrying a single-use
    token (10 minutes, bound to the MAC). Choosing one posts to
    `/agent/ipxe/start`, which creates and starts a deployment (superseding a
    running one, cancelling a pending one) and answers with the boot
    commands.
  - Debug mode (`requirePassword: false`): a key goes straight to the menu,
    with a banner. No password configured: only armed deployments run.
  - Every answer is a script, even on failure: it says why and returns to the
    firmware after 15 s. No iPXE shell.
- **Deployments** of Linux profiles are armed and validated as Windows ones
  (snapshot, expiry); a Linux deployment becomes `running` when its boot
  commands are served. Until decision 5 reports progress it can't finish on
  its own and ends at its expiry; decisions 3–5 ship together.
- **Import warns** about a 2023-only shim too ("machines without the
  Microsoft UEFI CA 2023 refuse it"), as it does for a 2011-only one.

## Consequences
- Linux and Windows clients share the DHCP answer, the iPXE chain and the
  decision order; only the script differs. Windows clients see no change.
- The gate stops re-install loops for Linux as ADR 0013 does for Windows:
  only armed deployments run without a person, and they're consumed.
- The password and the menu token cross the network in clear unless content
  HTTPS is on, as with the WinPE prompt.
- casper's ISO download is unauthenticated plain HTTP whatever the content
  listener does (the spike's B2 run); the deployment network is trusted, as
  for Windows content over HTTP.
- `/content/linux/<imageId>/` serves any ready Linux image to anyone who can
  reach the content listener, as boot image trees are today.
- To verify in the lab: iPXE's `prompt`, `login`, `menu` and `params` (POST)
  in the signed `snponly.efi` on the NUC, and that `exit` falls through to
  the installed disk.

## Implementation notes (2026-11-23)
- The generated script is served from the same path as the static one
  (`/content/boot/ipxe/<arch>/autoexec.ipxe`, `no-store`); the shared iPXE
  files around it are unchanged. Unknown clients get the default profile's
  script.
- The menu token is an agent session token (`agent_tokens`, kind `session`)
  with a 10-minute expiry, for the client found by MAC. It's consumed when
  presented, so a token presented with **another MAC is burnt** and the real
  client unlocks again.
- A Linux deployment armed for a machine that boots WinPE instead (a PXE
  boot, say) isn't handed to the agent at check-in; the WinPE prompt neither
  lists nor starts Linux profiles (`422 not_for_kind`).
- The ISO URL uses the address the request arrived on and that interface's
  HTTP port; with no plain HTTP listener there, the start answer says so and
  the deployment fails (`boot: …`).
- The form fields that may be absent are `nullable` in the spec, and
  `bootOrderAfterDeploy` lost its schema default (the server still uses
  `disk-first`): the request validator otherwise fills in null or the
  default, which Linux profiles refuse.

