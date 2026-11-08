#Requires -Version 7
<#
.SYNOPSIS
    Regenerates the archive test files in internal/drivers/archive/testdata.

.DESCRIPTION
    Windows only: uses makecab (MSZIP and LZX CABs) and tar.exe (bsdtar, 7z),
    both built into Windows. The tree is the same in each and is described in
    testdata/README.md.
#>
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'makecab and bsdtar 7z output need Windows.' }

$root = Split-Path -Parent $PSScriptRoot
$out = Join-Path $root 'internal/drivers/archive/testdata'
$src = Join-Path ([IO.Path]::GetTempPath()) "archive-fixture-src-$PID"
Remove-Item -Recurse -Force $src -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force "$src/drv/x64", $out | Out-Null

# The tree. Keep in sync with testdata/README.md and archive_test.go.
[IO.File]::WriteAllText("$src/drv/test.inf", "[Version]`r`nSignature=`"`$Windows NT`$`"`r`n")
$sys = [byte[]]::new(100KB)
for ($i = 0; $i -lt $sys.Length; $i++) { $sys[$i] = [byte](($i * 13) % 251) }
[IO.File]::WriteAllBytes("$src/drv/x64/test.sys", $sys)

foreach ($type in 'MSZIP', 'LZX') {
    $name = "test-$($type.ToLower()).cab"
    $ddf = Join-Path $src "$type.ddf"
    @"
.OPTION EXPLICIT
.Set CabinetNameTemplate=$name
.Set DiskDirectoryTemplate=$out
.Set CompressionType=$type
.Set Cabinet=on
.Set Compress=on
.Set RptFileName=NUL
.Set InfFileName=NUL
.Set DestinationDir=drv
"$src\drv\test.inf"
.Set DestinationDir=drv\x64
"$src\drv\x64\test.sys"
"@ | Set-Content -Encoding ascii $ddf
    makecab /F $ddf | Out-Null
    if ($LASTEXITCODE) { throw "makecab $type failed" }
    Write-Host ("wrote {0} ({1:N0} bytes)" -f $name, (Get-Item (Join-Path $out $name)).Length)
}

Push-Location $src
try {
    tar.exe --format 7zip -cf (Join-Path $out 'test.7z') drv
    if ($LASTEXITCODE) { throw 'tar 7z failed' }
} finally { Pop-Location }
Write-Host ("wrote test.7z ({0:N0} bytes)" -f (Get-Item (Join-Path $out 'test.7z')).Length)
Remove-Item -Recurse -Force $src
