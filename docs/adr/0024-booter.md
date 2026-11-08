# 24. Deciding what a client boots

Status: accepted (2026-11-08). Implements the boot decision of
[0013](0013-boot-prompt-password-gate.md) and the per-client content of
[0006](0006-prebuilt-bcd-per-boot-image.md).

## Context
Phases 1–3 answered every client with the same temporary files
(`-tftp-root`, `-boot-file`). ADR 0013 says what a client boots comes from
a profile: an armed deployment's, the client's own, or
`bootPrompt.defaultProfileId`. Profiles didn't exist yet, so this decision
also brings profile storage forward from decision 5.

## Decision
- **Order** (first match wins), per DHCP request after the architecture
  and MAC filters:
  1. a `pending` (armed) deployment for the client: its profile (from the
     snapshot when it names a boot image, else the profile);
  2. the client's profile;
  3. `bootPrompt.defaultProfileId`;
  4. otherwise no boot answer ("no profile to offer").
  A `running` deployment doesn't count: a client that reboots mid-deployment
  gets the prompt (ADR 0013).
- **No fallback past a broken choice.** If the chosen profile's boot image
  has no build, or its architecture doesn't match the client (x64 ↔
  `uefi-x64`, arm64 ↔ `uefi-arm64`), the client gets no answer and the log
  says why. Falling through to the default would boot a machine with
  something nobody chose.
- **Answer**: PXE clients get `bootmgfw.efi` from the boot image's tree,
  which becomes the client's root for TFTP and HTTP (ADR 0006). HTTP Boot
  and BIOS clients get no answer until HTTP Boot ISOs exist.
- **Sessions** (unchanged mechanism): DHCP records IP → MAC → answer for
  10 minutes; TFTP and HTTP look up the client's IP, then fall back to the
  lease (full mode, after a restart), then to the default profile's boot
  image (DHCP `off`: nothing tells clients apart). A session also records
  its tree, and content-store garbage collection keeps trees that live
  sessions use, so a rebuild or delete can't remove files mid-download.
- **No cache**: a decision is three or four indexed SQLite reads (well under
  a millisecond) a few times per boot; caching would only add invalidation.
- **Profiles** (spec as written): stored with every field; checked on
  create and replace: the boot image exists; the install image is a ready
  Windows image of the boot image's architecture and `editionIndex` is one
  of its editions; OS driver groups exist. `installMethod: smb` and
  `unattendTemplateId` are refused (`422 not_implemented`) until decision 4
  and Phase 5. Identity and secret checks come with decision 5.
  `clients.profile_id` becomes a foreign key; `defaultProfileId` must name
  a profile. Profiles, images, boot images and driver groups in use can't
  be deleted (`409`).
- The temporary `-tftp-root`, `-boot-file` and `-http-boot-url` flags are
  removed; `boot.Static` stays only for the DHCP protocol tests.

## Consequences
- A fresh install answers no one until a profile exists and is assigned or
  made the default (as ADR 0013 intended).
- Lab boots go through the API: import, build, profile, default, boot.
- A misconfigured profile shows up as "no boot answer" with a reason in
  the DHCP log; `POST /profiles/{id}/validate` (decision 5) will surface it
  before boot.
