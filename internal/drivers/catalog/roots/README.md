# Microsoft root CAs for driver catalogs

Driver catalogs signed by Microsoft (WHQL, attestation, inbox) chain to these
roots. Exported 2026-11-08 from the Windows trusted root store
(`Cert:\LocalMachine\Root`) by thumbprint; `catalog_test.go` checks them. A
DriverStore survey found catalogs chaining to the 2010 root (Third Party
Component CA 2012/2014, Production PCA 2011 and 2026) and the 2001 root
(Hardware Compatibility PCA); the other two cover older packs.

| File | Subject | SHA-1 thumbprint | Valid until |
|---|---|---|---|
| `microsoft-root-2010.crt` | Microsoft Root Certificate Authority 2010 | `3b1efd3a66ea28b16697394703a72ca340a05bd5` | 2035-06-23 |
| `microsoft-root-2011.crt` | Microsoft Root Certificate Authority 2011 | `8f43288ad272f3103b6fb1428485ea3014c0bcfe` | 2036-03-22 |
| `microsoft-root-2001.crt` | Microsoft Root Certificate Authority | `cdd4eeae6000ac7f40c3802c171e30148030c072` | 2021-05-10 |
| `microsoft-root-authority.crt` | Microsoft Root Authority | `a43489159a520f0d93d032ccaf37e7fe20a8b419` | 2020-12-31 |

Expired roots stay: Windows accepts timestamped signatures made while they
were valid, and the classifier ignores validity periods.
