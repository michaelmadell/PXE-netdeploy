#Requires -Version 7
<#
.SYNOPSIS
    Regenerates the UDF test images in internal/media/udf/testdata.

.DESCRIPTION
    Windows only: uses the IMAPI2 file system image writer built into Windows
    (no ADK or third-party tools). Writes one small image per UDF revision:
    1.02 (what Windows install media uses), 2.01, and 2.50 (metadata
    partition, which the reader must refuse), gzipped. The tree is the same in each and
    is described in testdata/README.md; the Go tests check against it.

.EXAMPLE
    ./scripts/make-udf-fixtures.ps1
#>
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'IMAPI2 is only available on Windows.' }

$root = Split-Path -Parent $PSScriptRoot
$out = Join-Path $root 'internal/media/udf/testdata'
$src = Join-Path ([IO.Path]::GetTempPath()) "udf-fixture-src-$PID"
Remove-Item -Recurse -Force $src -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $src, $out | Out-Null

# The tree. Keep in sync with testdata/README.md and udf_test.go.
New-Item -ItemType Directory -Force "$src/EFI/Boot", "$src/sources/sxs", "$src/Deep/a/b/c/d" | Out-Null
[IO.File]::WriteAllText("$src/EFI/Boot/bootx64.efi", 'not really a boot manager')
[IO.File]::WriteAllBytes("$src/empty.txt", [byte[]]@())
[IO.File]::WriteAllText("$src/Deep/a/b/c/d/leaf.txt", 'leaf')
[IO.File]::WriteAllText("$src/sources/Ünïcode-文件.txt", 'unicode name')
# 200 KiB + 5 bytes of a known pattern: byte i = (i * 7) mod 256.
$big = [byte[]]::new(200KB + 5)
for ($i = 0; $i -lt $big.Length; $i++) { $big[$i] = [byte](($i * 7) % 256) }
[IO.File]::WriteAllBytes("$src/sources/big.bin", $big)
# A directory with enough entries to span several sectors.
for ($i = 0; $i -lt 150; $i++) {
    [IO.File]::WriteAllText(("$src/sources/sxs/file-{0:D3}.cab" -f $i), "$i")
}

# PowerShell can't call IStream methods on the COM object directly.
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;
namespace UdfFixture {
    public static class StreamSaver {
        public static void Save(object comStream, string path) {
            var s = (IStream)comStream;
            STATSTG st;
            s.Stat(out st, 1);
            var buf = new byte[65536];
            IntPtr read = Marshal.AllocHGlobal(4);
            try {
                using (var fs = File.Create(path)) {
                    long left = st.cbSize;
                    while (left > 0) {
                        s.Read(buf, buf.Length, read);
                        int n = Marshal.ReadInt32(read);
                        if (n <= 0) break;
                        fs.Write(buf, 0, n);
                        left -= n;
                    }
                }
            } finally { Marshal.FreeHGlobal(read); }
        }
    }
}
'@

$revisions = @{ '102' = 0x102; '201' = 0x201; '250' = 0x250 }
foreach ($name in $revisions.Keys) {
    $fsi = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
    $fsi.FileSystemsToCreate = 4          # FsiFileSystemUDF only
    $fsi.UDFRevision = $revisions[$name]
    $fsi.VolumeName = "NDTEST_UDF$name"
    $fsi.FreeMediaBlocks = 0              # no size limit
    $fsi.Root.AddTree($src, $false)
    $result = $fsi.CreateResultImage()
    $iso = Join-Path ([IO.Path]::GetTempPath()) "udf$name-$PID.iso"
    [UdfFixture.StreamSaver]::Save($result.ImageStream, $iso)
    # Committed gzipped: the images are mostly padding.
    $path = Join-Path $out "udf$name.iso.gz"
    $in = [IO.File]::OpenRead($iso)
    $gz = [IO.Compression.GZipStream]::new([IO.File]::Create($path), [IO.Compression.CompressionLevel]::SmallestSize)
    try { $in.CopyTo($gz) } finally { $gz.Dispose(); $in.Dispose() }
    Remove-Item $iso
    Write-Host ("wrote {0} ({1:N0} bytes)" -f $path, (Get-Item $path).Length)
    [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($result)
    [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($fsi)
}
# IMAPI holds the source files until its objects are collected.
[GC]::Collect(); [GC]::WaitForPendingFinalizers()
Remove-Item -Recurse -Force $src -ErrorAction Continue
