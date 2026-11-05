# Test environments

Test against stock Microsoft keys first: that is what real hardware has.
Custom keys are useful for signing experiments, but a lab that only uses them
hides the problems you will hit in production.

## Hyper-V (Windows hosts)

`just lab` (elevated PowerShell) creates:

- an **internal** switch `NetDeployLab` with the host at `10.77.0.1/24` and no
  other DHCP server, so every DHCP answer comes from netdeployd;
- firewall rules for UDP 67/69/4011 and TCP 80/443 on that adapter only;
- a Gen 2 VM with Secure Boot, a vTPM, static memory, and network boot first.

Two Secure Boot templates, each trusting **one** Microsoft CA:

```powershell
just lab -VMName nd-win  -SecureBootTemplate MicrosoftWindows                   # Windows CA (PCA 2011) only
just lab -VMName nd-shim -SecureBootTemplate MicrosoftUEFICertificateAuthority  # third-party CA only
```

The `MicrosoftUEFICertificateAuthority` template replaces the Windows CA rather
than adding to it, so Windows boot managers are refused there (confirmed in the
Phase 1 spike). Use `nd-win` for the Windows path and `nd-shim` for shim/iPXE
(Phase 6). Most real PCs trust both CAs, which no Hyper-V template reproduces:
test that configuration on physical hardware.

Remove a VM with `just lab -VMName nd-win -Remove`.

Things to verify in the Phase 1 spike:

- DHCP broadcasts arrive on a socket bound to `10.77.0.1:67`;
- TFTP transfers aren't blocked by the firewall (data flows from ephemeral ports);
- which boot manager the VM accepts (2011 vs 2023 signed) with current templates.

## QEMU + OVMF (Linux hosts)

Use an OVMF build with Secure Boot (`OVMF_CODE.secboot.fd`). Two variable stores
are useful:

- **Microsoft keys:** your distro's pre-enrolled vars file (often
  `OVMF_VARS.ms.fd` or `OVMF_VARS.secboot.fd`). Check it includes the 2023 CAs.
- **Custom keys:** generate a PK/KEK/db with `openssl`, then enrol them with
  `virt-fw-vars` from the `virt-firmware` package (see `virt-fw-vars --help`
  for the enrolment options in your version).

Network boot needs a tap or bridge interface that netdeployd can serve DHCP on.
Details to be written up once the Linux host path is exercised in Phase 2.
