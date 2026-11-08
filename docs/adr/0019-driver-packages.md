# 19. Driver packages: archives, INF parsing, catalog verification

Status: accepted

## Context
Boot images need NIC and storage drivers injected (the I210 is why boot
image building is core), and the spec accepts driver archives with
per-INF enable/disable. A read-only spike over a Windows 11 DriverStore
(2026-11-08, 855 INFs) found:

- 786 INFs are UTF-16LE, the rest ANSI; 46,961 hardware IDs in models
  sections; decorations mostly `NTamd64`, some `NTarm64`, plus x86, ARM and
  IA64 leftovers.
- Third-party INFs carry a `CatalogFile`; inbox INFs don't (they're signed
  through the system catalog store).
- `smallstep/pkcs7` verifies every catalog present (125 WHQL, 17 other).
  Microsoft signs catalogs through several CAs (Windows Third Party
  Component CA 2012/2014, Hardware Compatibility PCA, and a new Microsoft
  Windows Production PCA 2026), so "signed by Microsoft" can't be read from
  the WHQL extended key usage alone.
- Of the folders in 52 CAB files Windows keeps, 77 are LZX, 48 MSZIP and 132
  uncompressed; the pure-Go CAB reader (`go-cabinet`) doesn't do LZX.

## Decision
- **Archives**: zip (standard library), 7z (`bodgit/sevenzip`), CAB
  uncompressed or MSZIP (`abemedia/go-cabinet`). LZX CABs are extracted with
  `expand.exe` (built into Windows) or `cabextract` (Linux);
  `/system/capabilities` reports `cabLzx`, and without a tool the import
  fails with `unsupported_archive` naming what to install. `.exe` and `.msi`
  are refused at `POST` (`400 unsupported_archive`), detected from their
  first bytes. Extraction stops at 32x the archive size or 200,000 files
  (`invalid_archive`). Files go into a content store tree (ADR 0015);
  packages deduplicate by the archive's SHA-256; uploads follow ADR 0017.
- **INF parsing** (own parser): UTF-16LE, UTF-8 and ANSI; comments,
  continuations, quoting and `%token%` substitution from `[Strings]`
  (preferring `.0409`). Per INF: class and role (`Net` network; `SCSIAdapter`
  and `HDC` storage), provider, `DriverVer` version and date, catalog,
  architectures and target OS versions from the decorations, and the
  hardware IDs for x64 and arm64. INFs for neither architecture are kept
  but disabled.
- **Catalog verification**: the PKCS#7 signature is verified
  (`smallstep/pkcs7`); the signer is chained through the catalog's own
  certificates to Microsoft root CAs compiled into the binary, ignoring
  validity periods (Windows accepts timestamped signatures from expired
  certificates). The INF and every file in its `[SourceDisksFiles]` must be
  members of the catalog with a matching hash (flat SHA-1/SHA-256, or the
  Authenticode hash for PE files). This is enforced for the INF and PE
  files; anything else the INF lists that isn't covered in a form we can
  check (a signed `.cab`, say) gives a package warning instead. Result: `whql` (chains to Microsoft,
  whatever the program: WHQL, attestation, inbox), `vendor` (valid, not
  Microsoft), `unsigned` (no or missing catalog, invalid signature, or a file
  not covered), `unknown` (can't be evaluated here), with `signatureDetail`
  saying why.
- **Storage**: `driver_packages` owns its tree; one `driver_infs` row per INF
  with `enabled`; hardware IDs indexed for the `hardwareId` filter; driver
  groups reference packages (not failed ones: `unknown_driver_package`),
  and a package in a group can't be deleted.
- **Space check** at `POST`: four times the archive size must fit above the
  reserve (`507`); the extraction limit catches the rest.

## Consequences
- Driver import needs no external tools except for LZX CABs.
- Phase 4 boot image validation refuses `unsigned` drivers and warns on
  `vendor` ones (x64 Windows with Secure Boot loads only Microsoft-signed
  kernel drivers).
- An INF supporting x64 and arm64 is one entry with both architectures
  (`DriverInf.architectures`), not two.
