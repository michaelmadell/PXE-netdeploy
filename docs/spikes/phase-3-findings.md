# Phase 3: findings

Measured behaviour and facts established while building storage and imports
(2026-11-06 to 2026-11-08). Procedures: [running-the-lab.md](../dev/running-the-lab.md).
Earlier results: [phase-2-findings.md](phase-2-findings.md),
[phase-1-findings.md](phase-1-findings.md). Decisions: ADRs 0015–0021.

All runs on the Windows dev machine (Go 1.27.3), local NVMe, loopback for
network paths. No lab VMs were needed in Phase 3.

## Windows install media (ADR 0018)

Win11 en-GB ISO, 8.9 GB, volume label `CPBA_X64FRE_EN-GB_DV9` (24H2/25H2
family).

| Fact | Detail |
|---|---|
| File system | UDF 1.02 bridge; the ISO9660 side is a stub. One type-1 partition, File Entries only, short allocation descriptors only |
| Extents | Most files are one extent; `install.wim` (8.0 GB) is eight (UDF's 1 GiB extent limit) |
| WIM compression | `boot.wim` and `install.wim`: LZX, 32 KiB chunks |
| Editions | 10, x64, `install.wim` (not ESD); `boot.wim` has 2 images |
| Boot managers | Five. Media root `EFI/Boot/bootx64.efi`: 2011-signed only. Inside `boot.wim` images 1 and 2: `Windows/Boot/EFI/bootmgfw.efi` (Windows PCA 2011) and `Windows/Boot/EFI_EX/bootmgfw_EX.efi` (Windows UEFI CA 2023, 3,087,200 bytes) |
| Libraries | `go-winio/wim` fails on the directory tree of current media; `nradchenko/go-wim` extracts `bootmgfw_EX.efi` read in place from the ISO in ~90 ms; `ejfkdev/udf` works but pulls ~150 modules |

Consequence: a boot image can serve the 2023-signed boot manager only by
taking it from inside `boot.wim`, which import already records (signer and
SHA-256 per boot manager, `details.bootManagers`).

## Upload and import

| Run | Result |
|---|---|
| tus upload, 8.9 GB ISO (tus-js-client, loopback) | 24 s; SHA-256 computed while receiving matches the file |
| Server killed mid-upload, then restarted | Resumed from the last 64 MiB checkpoint (`HEAD` offset); final hash matches |
| Image import of that upload | 14 s (13.6 s in the test harness), 1,002 blobs, no warnings |
| Import of an ISO already stored | Finishes at once (dedupe by the upload's hash) |
| Image delete | Sweep runs before the response; all space reclaimed |
| Same ISO through the real API end to end (Phase 3 backup run) | Upload + import 36–45 s |

## Driver packages (ADR 0019)

Read-only spike over this machine's DriverStore (`FileRepository`, 855 INFs):

| Fact | Detail |
|---|---|
| INF encoding | 786 UTF-16LE, the rest ANSI; parsed: all 855 |
| Architectures | 741 INFs for x64, 18 for arm64; x86/ARM/IA64 leftovers present. Undecorated and plain `NT` models sections are x86 only (Microsoft's rules), which the first parser got wrong |
| Hardware IDs | 46,961 in models sections |
| Catalogs | Third-party INFs carry `CatalogFile`; inbox ones don't. With the four pinned Microsoft roots: 142 `whql`, 2 `vendor`; 776/776 INF and PE files covered by their catalogs |
| Microsoft signing CAs seen | Windows Third Party Component CA 2012/2014, Hardware Compatibility PCA, Microsoft Windows Production PCA 2026, so "Microsoft-signed" can't be read from the WHQL EKU alone |
| Catalog format | `smallstep/pkcs7` returns the CTL without its outer SEQUENCE; the parser accepts both forms. CTL member digests come from `SpcIndirectDataContent` plus v1 UTF-16 hex tags |
| Coverage | A signed `.cab` shipped inside a driver isn't covered by any hash form we compute: coverage is enforced for the INF and PE files, others give a warning |
| CAB compression | Of the folders in 52 CAB files Windows keeps: 77 LZX, 48 MSZIP, 132 uncompressed. `go-cabinet` doesn't do LZX; `expand.exe` (Windows) and `cabextract` (Linux) do |

End to end (real server): a 4.5 MB zip of the Realtek Wi-Fi driver folder
and an LZX CAB made with `makecab` both import in 0.2 s as `whql` (signer
Microsoft Windows Hardware Compatibility Publisher). An `.exe` upload is
refused at `POST` with `400 unsupported_archive`.

## DISM and capabilities

- **DISM refuses to run unelevated, even for `/?`** (exit 740). Its version
  is read from the PE's `VS_FIXEDFILEINFO`: `10.0.26100.9549` here, while
  the file's version *string* still says `10.0.26100.8457` (stale string
  table). Use the binary field.
- `bcdedit.exe` and `expand.exe` are present in System32; `wimlib-imagex` is
  not installed on the dev machine.

## SMB backups (ADR 0020)

`cloudsoda/go-smb2` (fork of `hirochachacha/go-smb2`, pinned by commit; no
tagged releases) against two shares on this Windows 11 machine, NTLM logon
with a local account:

| Test | Result |
|---|---|
| Share requiring SMB encryption (`RejectUnencryptedAccess` on) | Connects (so the session is encrypted); 256 MiB write 278 MB/s, read 399 MB/s |
| Share without encryption | 173 MB/s write, 264 MB/s read (encrypted was faster: AES-GCM is hardware-accelerated) |
| Wrong password | `The attempted logon is invalid…` |
| Rename onto an existing file | Replaces it |
| Missing file | Matches `fs.ErrNotExist` |

Full cycle on real data (8.9 GB ISO + a driver package, 1,005 blobs):

| Step | Time |
|---|---|
| First backup to the encrypted share | 31 s (~288 MB/s) |
| Second backup, nothing new | 0.2 s |
| Backup by a different server of the same content | 0.7 s (blobs already at the share) |
| Restore from the share into an empty data directory | 27 s |
| Restored server | Image and driver package ready, storage health ok, its own backup to the share succeeds with the restored credential |
| Scheduled run (hourly at the next minute) | Fired at :00 of that minute; retention 2 kept two |

## Bugs found by tests and lab runs

| Bug | Fix |
|---|---|
| A panicked or interrupted job was marked failed before its resource was abandoned (`-race`, `TestFailureCodes`) | Abandon runs before the final status is recorded |
| All-zero UDF sectors passed the descriptor tag checksum | Tag id 0 is rejected |
| UDF `ReadDir` returned unsorted names | Sorted, as `fs.ReadDirFS` requires |
| Undecorated INF models sections counted as x64 | x86 only |
| A schedule change swallowed a slot that passed before the scheduler's next check | Slots counted from the previous check; checks every 15 s |
| A restored server reported its backup `interrupted` (the snapshot holds its own running job) | The job is marked succeeded inside the snapshot copy |
| Restoring with `-key` still warned `secrets_key_not_exported` | Importing a key records the export |

## Other facts worth keeping

- **Go's `time.Date` on a repeated local time** (clocks going back) picks
  the second instance; a daily slot at 01:30 on 2026-10-25 runs at 01:30 GMT,
  once.
- **SQLite can't change a CHECK** without rebuilding the table, and with
  foreign keys on, dropping the old table fires `ON DELETE` actions (it
  would have cleared `uploads.job_id`). Migrations can now start with
  `-- netdeploy: foreign_keys=off` (checked with `foreign_key_check` before
  commit).
- **Windows PowerShell 5.1 strips the quotes** from JSON passed inline to
  `curl.exe`; send bodies from a file, or use PowerShell 7's
  `Invoke-RestMethod`.
- **Heredocs through the device bridge** turn `﻿` escapes into literal
  BOMs in Go source; write them with Python.
