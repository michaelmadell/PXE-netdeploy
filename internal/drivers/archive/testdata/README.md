# Archive test files

Generated on Windows by `scripts/make-archive-fixtures.ps1` with `makecab`
and `tar.exe` (bsdtar), both built into Windows. Same tree in each:

```
drv/test.inf       "[Version]\r\nSignature=\"$Windows NT$\"\r\n"
drv/x64/test.sys   100 KiB, byte i = (i*13) mod 251
```

| File | Format |
|---|---|
| `test-mszip.cab` | CAB, MSZIP (read in Go) |
| `test-lzx.cab` | CAB, LZX (needs expand.exe or cabextract) |
| `test.7z` | 7z (bsdtar's default codec) |
