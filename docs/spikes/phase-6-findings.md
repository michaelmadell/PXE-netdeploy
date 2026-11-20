# Phase 6: findings

Measured behaviour and facts established while building Phase 6 (from
2026-11-18). Earlier results: [phase-5-findings.md](phase-5-findings.md).
Decisions: ADR 0028 onwards.

## Content HTTPS (decision 1, ADR 0028)

**Automated, 2026-11-18** (`go test -count=1 ./...` on the Windows lab
host, `just lint`, `just build`):

- `TestEnsureLoadAndIssue`: no CA (and no secrets key) until `Ensure`; the
  CA is RSA-3072, ten years; `content-ca-key.enc` holds no PEM; the server
  certificate's `notBefore` is the CA's and it verifies for its IP and DNS
  names (case and trailing dot normalised) and not for others; reloading
  gives the same CA.
- `TestNamesChangeReissues`: names re-read after 10 s; a new address gets a
  new certificate; renewed 30 days before expiry.
- `TestRestoreWithoutKey`: the CA files without the secrets key are
  reported lost and replaced by a new CA.
- `TestAgentTrust`: the agent's TLS config accepts the server with its
  clock at 2001-01-01, rejects an expired certificate and another CA.
- `TestContentHTTPS` (netsvc): HTTPS and HTTP listeners side by side,
  HTTP/1.1 only even when the client offers h2, the certificate from
  `GetCertificate`.
- `TestContentHTTPS` (settings): a custom pair refused; HTTPS on the HTTP
  port refused; enabling HTTPS makes the CA, starts the listener and
  reports `contentHttps.ca`/`certificate` covering the interface address and
  `advertisedAddress`.
- `TestCheck` (`mixed_server_urls`), `TestDiff` (HTTP inputs' JSON has no
  `contentCa`, so their hashes are unchanged), `TestContentCAInput`
  (refused without a CA; `contentCaPem` in `config.json`; stale
  `content-ca` when the CA changes; HTTP images unaffected).
- `TestAgentReachable`/`TestAgentOverHTTP`, `TestContentCAEndpoint` (404,
  PEM, DER), `TestContentCAWarning`.

**Lab, 2026-11-18, `nd-client-01`** (the user, Hyper-V, Secure Boot, 2023
boot manager):

| Run | Result |
|---|---|
| HTTPS on the lab interface, boot image rebuilt with `https://10.77.0.1`; armed deployment: profile `1`, Windows 11 Pro from Win 11 26H2, local accounts, the `Lab: first logon` template, `site=Lab`, computer name `LAB-1-05` | **Worked.** Ran 14:17 to 14:20, 3 minutes end to end, including the 7.6 GB `install.wim` download over TLS. No noticeable difference from the HTTP runs: TLS costs nothing visible on the lab's internal switch |
| Physical machine with an **Intel I210**, cabled to a USB Ethernet adapter that `NetDeployLab` was made an External switch on (host still `10.77.0.1`); same HTTPS boot image, **no I210 driver** in it | **Worked.** Network boot under Secure Boot, TFTP at the defaults, the agent over HTTPS, deployment finished. The 7.6 GB `install.wim` took **about 75 s**, line rate for 1 Gbit/s: slower than Hyper-V only because of the link. boot.wim over TFTP took roughly 12–15 s. Plain WinPE (boot.wim index 1) also had network. Also closes Phase 1 tasks 7–8 (WinPE's inbox driver covers the I210) |

**Not yet tested in the lab:**

| Check | How |
|---|---|
| `secrets_key_not_exported` | Appears if the key was never exported |
| Clock set back | Hyper-V's RTC follows the host, so on physical hardware: set the firmware clock before the CA's start and deploy; the agent should still connect (unit-tested only) |
| `agent_over_http` | A profile whose boot image keeps `http://10.77.0.1` while HTTPS is on |
| DER download | Settings → Admin → DER; opens as a certificate in Windows |

## Admin authentication (decision 3, ADR 0029)

**Automated, 2026-11-18** (`go test -count=1 ./...`, `just lint`,
`just build` on the Windows lab host):

- `TestAuthSetupAndSignIn`: before setup only the open operations answer
  (`401 unauthenticated` otherwise, unknown paths included, and
  `/api/v1/agent/../clients`); health without a session has statuses only;
  sign-in answers `409 setup_required`; a wrong setup code `403`, another
  site's Origin `403 bad_origin`; the right code (any case) creates the
  account with an `HttpOnly`, `Secure`, `SameSite=Strict` `__Host-` cookie and
  deletes `setup-code.txt`; a second setup `409`. Cookie changes need the
  Origin; a token (`ndt_…`) works without one, can't reach `/users` or
  `/tokens` (`403 not_for_api_tokens`), and records `lastUsedAt`. Sign-out
  clears the cookie and ends the session; 8 hours idle ends it.
- `TestSignInRateLimit`: 6 failures, then `429` with `Retry-After`, also
  from another address for that username; allowed again after the back-off;
  an unknown username answers like a wrong password.
- `TestAccounts`: duplicate usernames ignoring case, short passwords;
  changing one's own password keeps that session and ends the others; a
  password set by another account ends all of them; deleting an account
  revokes its tokens; the last account can't be deleted.
- `TestDevOrigin`: `-dev` cookie without `Secure`, Origin matching
  `X-Forwarded-Host` accepted, another origin refused.
- `TestIdempotencyKeysPerCaller`: the same key from another account isn't
  replayed. `TestPublicHealth`.
- `TestSessionsEndAtOnce` (real HTTP server, open `/events` streams):
  changing one's own password sends `session.ended` (`password_changed`) to
  that account's other session, whose stream closes, and nothing to the
  session that changed it or to another account; the admin setting the
  password ends the account's remaining session the same way.
  `TestStreamNoticesIdleSession`, `TestStillValid`: an idle session's stream
  ends with `expired` at the next heartbeat.

**Built binary, 2026-11-18** (`.tmp/auth-smoke.ps1`, a fresh data directory,
admin on 127.0.0.1:18443): setup code logged and in the file; anonymous
session `setupRequired`, `/clients` 401, health statuses only, UI 200; setup
with the code 201 and the cookie stored by curl; `/clients` with the cookie
200; a token without Origin 403 `bad_origin`, with it 201; the token on
`/users` 403 `not_for_api_tokens`, on health the full document; the request
log names `cookie:labadmin` and `token:labadmin/smoke`.

**Lab, 2026-11-18** (the user, against `C:\netdeploy\data`): these
**worked**:

| Check | How |
|---|---|
| Upgrade of the lab server | Start on `C:\netdeploy\data`: migration 0014, a setup code in the log; create the account in the UI |
| The web UI end to end | Setup page, sign-in, sign-out, Accounts page (add, set password, delete, tokens); `/events` and uploads with the cookie |
| Kicked out at once | Two browsers signed in as the same account; change the password in one: the other shows the sign-in page within a second. Same for deleting the account and for signing out in another tab |
| `netdeployd admin reset-password` | Server stopped; then sign in with the new password |

**Not yet tested:**

| Check | How |
|---|---|
| `just dev` over the LAN | `-dev` and the Vite proxy's `X-Forwarded-Host` |
| Lab scripts with a token | `running-the-lab.md` now sets `Authorization` for `Invoke-RestMethod` |

## Linux host (decision 4, ADR 0030)

**Automated, 2026-11-19:**

- Windows lab host: `go test -count=1 ./...`, `just lint`, `just build`.
  `TestWimlibSetupImage` with the real wimlib-imagex 1.14.5: a synthetic
  two-image boot.wim (boot index 2) exported at index 2, updated with
  `config.json` and `winpeshl.ini`, exported again: one bootable image, still
  the Setup image, with the added files and the 2023 boot manager.
  `TestICMPPinger` used a raw socket (unelevated shell).
- **On a Linux kernel** (the Cowork Linux VM on the lab host: Ubuntu 22.04,
  kernel 6.8, an ordinary user, no capabilities, `ping_group_range` closed):
  test binaries cross-compiled with `GOOS=linux CGO_ENABLED=0` for `hostcheck`,
  `portowner`, `dhcp`, `netsvc`, `server`, `settings`, `store`, `bootimage`,
  `contentca` all passed. The probe reported `lowPorts: false`, `icmp: none`,
  missing `CAP_NET_BIND_SERVICE` and `CAP_NET_RAW`, as expected for that
  user. `TestLookupOwnSockets` found the test's own TCP and UDP sockets
  through `/proc`; `TestLookupProc` a fake `/proc` with dnsmasq on UDP 67.
- **The Linux binary** on that VM (`scripts/linux-smoke.sh` with
  `NETDEPLOY_SMOKE_NO_TOOLS=1`: no wimtools or cabextract there): data
  directory `0700`, `setup-code.txt` `0600` and in the log, setup, token,
  `hostOs: linux`, DISM unavailable with its reason, `privileges` as above,
  health `ok` with no `data_dir_permissions`. **Passed.**

**Lab VM, 2026-11-19** (the user: `nd-linux`, Ubuntu, `netdeploy` service
account with the two capabilities, systemd unit from `running-the-lab.md`):

| Check | Result |
|---|---|
| Capabilities | **As expected**: `lowPorts: true`, ICMP raw, nothing missing |
| Port owner | **Worked**: the `422` named the process holding the port |
| `data_dir_permissions` | **Worked in the API** (the directory at `755` named with its mode and the fix). The Overview listed it only under "Things to check" at the bottom, with the status still "ok", so it looked missing. Changed: the status reads "ok, N things to check" in the warning colour and the list sits under it |
| File modes | SQLite created `netdeploy.db` `644` (and `admin-cert.pem` `644`): readable by anyone once the directory is opened. Changed: the server sets umask `077` at start, the check covers the database, the lab unit has `UMask=0077` |
| The rest, after the fixes | **All good** (the user, 2026-11-19, "all is good from linux"): the remaining checks from `running-the-lab.md`, "A Linux host", including the Overview's status with warnings and the database at `600` |
| Lab setup | A stuck host adapter on `NetDeployLab` ("media disconnected" while Internal) and `eth1` unmanaged by networkd delayed the run; the service's first start failed with `203/EXEC` (systemd couldn't run the binary) |

**Not yet run:**

| Check | How |
|---|---|
| Windows `ownerProcess` on a real conflict | E.g. HTTP 80 held by `http.sys` (expect `System (pid 4)`) |

## Packaging (decision 5, ADR 0031)

**Automated, 2026-11-19** (Windows lab host: `go test -count=1 ./...`,
`just lint`, `just package -RequireInstaller`; Linux: cross-compiled tests in
the Cowork VM):

- `TestRenewGenerated`, `TestKeepCustom`: the generated admin certificate is
  kept a year before expiry, renewed 20 days before (old pair kept as
  `.old`); a certificate with another name is never replaced.
- `TestDataDirACL` (Windows): a data directory `datalock` creates has a
  protected list (SYSTEM, Administrators, the creating account); one where
  Users can read is reported with the `icacls` fix.
- The Windows binary, unelevated: `service install` says to run elevated;
  `admin set-address` on a new folder creates it private and stores the
  address; the server then listens there and logs to `-log-file`.
- The Linux binary: `admin set-address` on a fresh data directory, then
  serving on that address; a bad address refused; `service` explains it's
  Windows only; `scripts/linux-smoke.sh` passes.
- Packages built: `netdeploy-0.0.0-81-g1675124-dirty-setup.exe` (12 MB,
  with wimlib 1.14.5) and the `.deb`s (amd64 15 MB, arm64 14 MB). The
  `.deb`: control fields, conffile, scripts, debconf templates as intended
  (`dpkg-deb -I/-c`); maintainer scripts parse (`sh -n`); the unit passes
  `systemd-analyze verify`.

**Not yet run:**

| Check | How |
|---|---|
| The service's recovery actions | Kill `netdeployd.exe` and watch the SCM restart it |
| The release workflow | Push a `v*` tag: wimlib download and hash check, ISCC from Chocolatey, the GitHub release |

**Lab, 2026-11-19** (the user): **the packages work**: the Windows
installer and the `.deb`.

**First CI run, 2026-11-19:** the `.deb` installed and the service started,
but `deb-smoke.sh` failed "admin not on all networks": given `0.0.0.0:8443`,
Go listens dual-stack (the log says `https://[::]:8443`) and `ss` prints
`*:8443`, not `0.0.0.0:8443`. The check now accepts `*`, `[::]` and
`0.0.0.0`. The Windows job was cancelled by the matrix's fail-fast, not
broken; CI now runs with `fail-fast: false`.

**CI, 2026-11-19, after the fix: passes on Ubuntu and Windows.** On Ubuntu
that covers the Linux host checks (wimtools and cabextract,
`TestWimlibSetupImage` with the distribution's wimlib, the datagram ICMP
socket, `scripts/linux-smoke.sh` with the tool checks) and the `.deb`
cycle in `scripts/deb-smoke.sh` (install as root with systemd, the unit's
capabilities, reconfigure to 127.0.0.1:9443, purge moving the data aside,
a fresh reinstall).

## HTTPS boot spike (decision 2), stages 1–2: QEMU/OVMF

**2026-11-19**, on the Windows lab host: QEMU 10 (winget), Debian's OVMF
2025.02 (`OVMF_CODE_4M.fd`, no Secure Boot), QEMU's user-mode network (the
host is `10.0.2.2`), the boot URL from its DHCP (`bootfile=`). A throwaway
spike tool (`.tmp/httpboot-spike`, not committed) fetched the default
profile's built boot image from the server's HTTP content listener, packed
it, and served it over HTTP and HTTPS (its own RSA-3072 CA, RSA-2048 server
certificate), logging every request and TLS ClientHello.

| Run | Result |
|---|---|
| QEMU's bundled OVMF (Windows build) | No network boot at all: no network stack in that build |
| Debian OVMF, no RNG device | Still none: since the PixieFAIL fixes the EDK II network stack needs an RNG and isn't loaded without one ([RHEL-58631](https://issues.redhat.com/browse/RHEL-58631)). `-device virtio-rng-pci` fixes it; `romfile=` keeps iPXE out so the firmware's own virtio-net driver and HTTP Boot are tested |
| FAT32 image **without a partition table** (`.img`, `application/vnd.efi-img`) | Downloaded (713 MiB in 41 s), then "Invalid Parameter": not recognised, not even as a local disk |
| **MBR disk, one EFI System Partition, FAT32** (`EFI/BOOT/BOOTX64.EFI` = the boot manager, the BCD at `EFI/Microsoft/Boot/BCD`, `Boot/boot.wim`, `Boot/boot.sdi`, fonts) over **HTTP** | **Worked.** `HEAD` then `GET` (`UefiHttpBoot/1.0`, 713 MiB in 45 s), RAM disk, the boot manager, **our BCD unchanged** (it names the boot device, not TFTP), boot.wim, Windows PE, the netdeploy agent. (The agent found no network adapter: WinPE has no virtio-net driver; a QEMU artefact.) Under WHPX the VM stopped in the Windows kernel ("Unexpected VP exit code 4"); TCG (`-cpu max`) ran through |
| Same over **HTTPS**, the CA given to OVMF (`-fw_cfg name=etc/edk2/https/cacerts`, an `EFI_SIGNATURE_LIST`) | First **failed**: the firmware offers **TLS 1.2 at most, RSA and DHE key exchange only, no ECDHE** (`DHE-RSA-AES-GCM/CBC`, `RSA-AES-GCM`, `RSA-AES-CBC-SHA256/SHA`); Go has no DHE and leaves RSA key exchange out of its defaults since 1.22: "no cipher suite supported by both". With `TLS_RSA_WITH_AES_128_GCM_SHA256` and `..._256_GCM_SHA384` added: **worked**, `TLS_RSA_WITH_AES_128_GCM_SHA256`, 713 MiB in 45 s (as fast as HTTP) |
| HTTPS **without** the CA | Handshake abandoned after the certificate (`EOF`), nothing downloaded: the firmware verifies |
| ISO, Windows-media layout (IMAPI2: UDF + Joliet), El Torito `efisys_noprompt.bin` (cdboot) | Downloaded, then "No mapping": cdboot can't use a CD that exists only as a firmware RAM disk |
| ISO whose El Torito image is the whole ESP | The boot manager started from the El Torito partition, then "BCD missing" (`0xc000000f`): on a CD it takes **the whole disc** as its boot device and reads its file system, not the partition |
| **ISO, Windows-media layout, El Torito = a small FAT image holding our boot manager as `BOOTX64.EFI`** (BCD at `\EFI\Microsoft\Boot\BCD`, `\Boot\boot.wim`, `\Boot\boot.sdi` in the disc's UDF file system; 632 MB) | **Worked** to the Windows kernel (then WHPX's usual stop, as with the disk image) |
| Same layout, **plain ISO 9660 only** (go-diskfs: no UDF, no Joliet) | "BCD missing": go-diskfs had truncated `MICROSOFT` to `MICROSOF` (level-1 names) |
| The same disc with that one directory record patched to `MICROSOFT` | **Worked** to the Windows kernel: the boot manager reads plain ISO 9660 (names matched ignoring case, `;1` versions fine); it needs names longer than 8 characters, not UDF |

Consequences for building it:

- The image is an **MBR + ESP disk image**, not a superfloppy; an ISO only
  if real firmware needs it.
- The content HTTPS listener must **add the two RSA-GCM suites** (at least
  for HTTP Boot requests), or firmware can't connect. RSA key exchange has
  no forward secrecy; it is what firmware speaks.
- The image is about the size of boot.wim plus 10 %, held in RAM twice by
  the client (the RAM disk, then boot.wim): 4 GB was enough.

- The image is an **ISO** in that last layout (the most widely supported
  RAM-disk format; the disk image works in OVMF only), built in Go: plain
  ISO 9660 with names up to 31 characters is enough (IMAPI2 is
  Windows-only and far too slow with a large boot image; go-diskfs
  truncates names to 8.3).

## HTTPS boot spike, stage 3: Intel NUC (i5-1335U, I210 + I219), 2026-11-20

The user, with the spike server on the lab host (`https://10.77.0.1:8444`),
the spike CA (`ca.der`) enrolled in the NUC's setup, the URL typed into its
HTTPS Boot setting (its HTTP Boot is HTTPS only).

| Run | Result |
|---|---|
| TLS | **TLS 1.3**, `TLS_AES_128_GCM_SHA256`, ECDHE offered: unlike QEMU's OVMF, this firmware needs nothing beyond Go's defaults. The enrolled CA was accepted |
| `netdeploy.img` (MBR + ESP), three times | Downloaded (713 MiB in 27–30 s), then back to setup |
| `netdeploy2.iso` (the layout that works in QEMU) | Downloaded (632 MB in 24 s), then back to setup |
| `bootmgfw.efi` alone | **Started** (Secure Boot on): black screen, as expected with no BCD to read over HTTP |
| `netdeploy2.iso` with **Secure Boot off** | Back to setup again |

**Conclusion:** this firmware's HTTPS Boot runs EFI programs only; it doesn't
boot RAM-disk images (ISO or disk), whatever Secure Boot is set to. The
boot manager can't fetch its BCD and boot.wim over HTTP itself (it needs
PXE/TFTP), so on such firmware WinPE can only be HTTPS-booted through a
signed intermediate loader that does the RAM disk (shim + iPXE `sanboot`,
the non-Windows phase), not through firmware alone.

## HTTPS boot spike, stage 4: iPXE shim chain on the NUC, 2026-11-20

Same NUC, same spike server and CA, Secure Boot **on**. The URL is
`https://10.77.0.1:8444/ipxe-shim.efi`. Files served flat from one directory:

| File | Signed by | Source |
|---|---|---|
| `ipxe-shim.efi` | Microsoft Corporation UEFI CA 2011 (third-party CA) | iPXE v2.0.0 `ipxeboot.tar.gz`, `x86_64-sb/shimx64.efi` |
| `ipxe.efi` | iPXE Secure Boot CA (production, "Automatic Code Signing G1A") | same archive, `x86_64-sb/ipxe.efi` |
| `wimboot` | Microsoft Corporation UEFI CA 2011 | wimboot v2.9.0 |
| `autoexec.ipxe` | — | ours: `kernel wimboot gui`, `initrd` bootmgfw.efi, BCD, boot.sdi, boot.wim, `boot` |
| `bootmgfw.efi`, `BCD`, `boot.sdi`, `boot.wim` | — | the existing boot image, unchanged |

**Result: WinPE booted** (the user: "Boots perfect, and slightly faster").

What the server log shows:

1. The firmware (`UefiHttpBoot/1.0`, TLS 1.3) sends HEAD + GET for `ipxe-shim.efi` (1 MB in 44 ms).
2. The shim then fetches `revocations_sku.efi`, `revocations_sbat.efi` and `shim_certificate_0.efi` from the same directory. Returning 404 is fine; these are optional.
3. The shim then fetches `ipxe.efi` over the firmware's HTTP stack.
4. iPXE 2.0.0 opens **one** TLS 1.2 connection (`ECDHE_RSA_AES_128_GCM_SHA256`; it offers no TLS 1.3) and fetches `autoexec.ipxe` from its own directory. It trusts the CA held in the firmware's `TlsCaCertificate` variable, so no certificate needs embedding.
5. On the same keep-alive connection it fetches `wimboot`, `bootmgfw.efi`, `BCD` and `boot.sdi`, then `boot.wim` (599 MB in 17.6 s, ~34 MB/s, compared with the 632 MB ISO in 24 s).
6. wimboot hands the files to the boot manager in RAM. Our BCD (paths relative to `bootDevice()`) works unchanged.

**Consequences for decision 2:**

- No RAM-disk image needs building or storing. The HTTPS URL serves the same files as PXE, plus the shim, `ipxe.efi`, wimboot and a generated script.
- This works on firmware that only runs EFI programs (the NUC). It should also work on OVMF, which still needs the RSA-GCM suites for its own TLS 1.2 fetch of the shim.
- It requires the firmware to trust Microsoft's **third-party** UEFI CA. Machines locked to the Windows CA only (some Secured-core PCs) need that option enabled, or need plain PXE.
- Under Secure Boot, only the precompiled signed `ipxe.efi`/`snponly.efi` can be used, not a custom build. The boot.ipxe.org bundle is test-signed only.
- iPXE's TLS is 1.2 with ECDHE or RSA suites, so Go's defaults suffice for that part.

**Signatures checked afterwards** (Authenticode certificate tables):

| File | Signed by |
|---|---|
| iPXE v2.0.0 archive's `shimx64.efi` / `shimaa64.efi` (stage 4) | Microsoft UEFI CA **2011 only** |
| `ipxe/shim` ipxe-16.1 `ipxe-shimx64.efi` / `ipxe-shimaa64.efi` | Microsoft UEFI CA 2011 **and 2023** |
| wimboot v2.9.0 `wimboot` / `wimboot.arm64` | Microsoft UEFI CA 2011 and 2023 |
| iPXE v2.0.0 `x86_64-sb/ipxe.efi`, `arm64-sb/ipxe.efi` | iPXE Secure Boot CA (production, G1A) |

Firmware trusting only the 2023 CAs refuses the archive's shim, so ADR 0032
bundles the ipxe-16.1 shims. Both builds have the same SBAT data
(`shim,4`, `shim.ipxe,1`); only their signatures differ.

**Re-test on the NUC.** Its DB holds Microsoft UEFI CA 2011 and 2023, Windows
UEFI CA 2023, Windows Production PCA 2011, Option ROM UEFI CA 2023 and an
ASUS key.

- The first boot with the ipxe-16.1 shim (08:57) got a firmware **"Secure
  Boot Violation"** box. It came after the whole chain had loaded: the log
  shows the shim fetching `ipxe.efi`, then iPXE fetching every file through
  `boot.wim`. So the firmware refused wimboot or `bootmgfw.efi` when they
  were started, not the shim.
- It couldn't be reproduced. Boots at 09:23 and 09:27 with the 2011-only
  shim, and at 09:26 and 09:28 with the dual-signed shim, all worked.
- The one difference in the log: the 08:47 and 08:57 boots had the shim
  ask for `revocations_sku.efi` and `revocations_sbat.efi`, and every boot
  since asks only for `shim_certificate_0.efi`. Something persistent changed
  at that boot, most likely the shim writing its revocation state
  (`SbatLevel`) on its first run. The violation is put down to that one-time
  update.

Still to try: a boot with the 2011 CA removed from DB, to prove the
2023-only case.

## HTTPS boot, built (decision 2, ADR 0032)

Built 2026-11-20:

- `internal/ipxe` reads `<dir>/<arch>/{ipxe-shim.efi, ipxe.efi, wimboot}` at
  start and generates `autoexec.ipxe` per architecture.
- Content listeners serve the chain at `/content/boot/ipxe/<arch>/` for every
  client, ahead of the client's tree.
- `boot.Profiles` answers HTTP Boot architectures with the shim's path and
  the same tree as PXE. DHCP turns that into
  `https://<addr>[:port]/content/boot/...` (or `http://`), or no answer with
  the reason.
- `capabilities.httpBoot` and the `http_boot_unavailable` health warning.
- Content TLS now always adds the two RSA-GCM suites (tested with an RSA-kx
  client).
- `scripts/fetch-ipxe.ps1` (`just fetch-ipxe`) pins every hash. Both packages
  ship the chain: `{app}\ipxe`, and `/usr/lib/netdeploy/ipxe`, whose
  directories are 0755 because globs are used rather than a tree. `deb-smoke`
  checks that capabilities list both architectures.

**Lab, 2026-11-20** (the user, the NUC): **worked.** netdeployd answered
the NUC's HTTPS Boot request over its own DHCP, with no URL typed in. The
NUC trusted the content CA, enrolled as DER, and loaded the bundled
dual-signed shim → `ipxe.efi` → wimboot → the default profile's boot
image to WinPE, with Secure Boot on.

**Not yet run:**

| Check | How |
|---|---|
| 2023-only DB | Untick the Microsoft UEFI CA 2011 in the BIOS and boot again |
| Proxy-mode answer | The same boot with DHCP `proxy` and another DHCP server |
| OVMF against netdeployd | The RSA-GCM suites end to end (unit-tested; proven in the spike) |
| arm64 | No lab machine |

