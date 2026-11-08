# UDF test images

Generated on Windows by `scripts/make-udf-fixtures.ps1` with the IMAPI2 file
system image writer (built into Windows), gzipped (the images are mostly
padding). Same tree in each; only the UDF revision differs:

| File | UDF revision | Reader |
|---|---|---|
| `udf102.iso.gz` | 1.02 (as Windows install media; File Entries) | must read |
| `udf201.iso.gz` | 2.01 (Extended File Entries) | must read |
| `udf250.iso.gz` | 2.50 (metadata partition) | must refuse with `ErrUnsupported` |

Tree:

```
EFI/Boot/bootx64.efi          "not really a boot manager"
empty.txt                     0 bytes
Deep/a/b/c/d/leaf.txt         "leaf"
sources/Ünïcode-文件.txt       "unicode name" (16-bit d-string)
sources/big.bin               200 KiB + 5 bytes, byte i = (i*7) mod 256
sources/sxs/file-000.cab ...  150 files, content "0".."149" (multi-sector directory)
```
