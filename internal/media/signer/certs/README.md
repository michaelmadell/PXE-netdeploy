# Microsoft Secure Boot CA certificates

The CAs a UEFI `db` trusts for Windows and third-party boot binaries. Public
certificates, downloaded 2026-11-08 from Microsoft's links in
[Windows Secure Boot key creation and management guidance](https://learn.microsoft.com/windows-hardware/manufacture/desktop/windows-secure-boot-key-creation-and-management-guidance).
`signer_test.go` checks the thumbprints.

| File | Subject | SHA-1 thumbprint | Valid until | Link |
|---|---|---|---|---|
| `windows-pca-2011.crt` | Microsoft Windows Production PCA 2011 | `580a6f4cc4e4b669b9ebdc1b2b3e087b80d0678d` | 2026-10-19 | https://go.microsoft.com/fwlink/p/?linkid=321192 |
| `uefi-ca-2011.crt` | Microsoft Corporation UEFI CA 2011 | `46def63b5ce61cf8ba0de2e6639c1019d0ed14f3` | 2026-06-27 | https://go.microsoft.com/fwlink/p/?linkid=321194 |
| `windows-uefi-ca-2023.crt` | Windows UEFI CA 2023 | `45a0fa32604773c82433c3b7d59e7466b3ac0c67` | 2035-06-13 | https://go.microsoft.com/fwlink/?linkid=2239776 |
| `uefi-ca-2023.crt` | Microsoft UEFI CA 2023 | `b5eeb4a6706048073f0ed296e7f580a790b59eaa` | 2038-06-13 | https://go.microsoft.com/fwlink/?linkid=2239872 |

The 2011 CAs have expired, but UEFI firmware doesn't check validity periods,
so neither does the classifier.
