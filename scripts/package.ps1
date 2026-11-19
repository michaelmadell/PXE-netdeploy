#Requires -Version 7.2
<#
.SYNOPSIS
  Release packages into ./dist (ADR 0031): the Windows installer
  (netdeploy-<version>-setup.exe, needs Inno Setup's ISCC) and the .deb
  packages for amd64 and arm64 (nfpm, run through `go run`).
.PARAMETER SkipBuild
  Use the binaries already in ./dist instead of running build.ps1.
.PARAMETER WimlibDir
  The unzipped wimlib Windows release to bundle (default .tmp\wimlib).
.PARAMETER Iscc
  Path to ISCC.exe if it isn't in the usual places.
.PARAMETER RequireInstaller
  Fail instead of skipping the installer when ISCC or wimlib is missing (CI).
.NOTES
  Signing: set NETDEPLOY_SIGN to a sign command for ISCC's /S option, e.g.
  'signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /f cert.pfx $f'.
#>
param(
    [switch]$SkipBuild,
    [string]$WimlibDir = '.tmp/wimlib',
    [string]$Iscc,
    [switch]$RequireInstaller
)
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$nfpm = 'github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.49.0'
# nfpm needs a newer Go than the repo's pinned 1.27.3: let go fetch it for this tool.
$env:GOTOOLCHAIN = 'auto'

# --- Version (ADR 0031) ------------------------------------------------------
$ver = & ./scripts/version.ps1
$v, $debVersion, $fileVersion = $ver.Version, $ver.Deb, $ver.File
Write-Host "Version $v (deb $debVersion, file $fileVersion)" -ForegroundColor Cyan

if (-not $SkipBuild) {
    ./scripts/build.ps1
    if ($LASTEXITCODE) { throw 'build failed' }
}

# --- .deb ---------------------------------------------------------------------
foreach ($arch in 'amd64', 'arm64') {
    $bin = "dist/netdeployd-linux-$arch"
    if (-not (Test-Path $bin)) { throw "$bin missing: run without -SkipBuild" }
    # nfpm doesn't expand variables everywhere (not in file paths): fill them in.
    New-Item -ItemType Directory -Force .tmp | Out-Null
    $config = ".tmp/nfpm-$arch.yaml"
    (Get-Content -Raw packaging/linux/nfpm.yaml).Replace('${NETDEPLOY_ARCH}', $arch).
        Replace('${NETDEPLOY_DEB_VERSION}', $debVersion).Replace('${NETDEPLOY_BIN}', $bin) |
        Set-Content -NoNewline $config
    go run $nfpm package -f $config -p deb -t dist/
    if ($LASTEXITCODE) { throw "nfpm failed for $arch" }
}

# --- Windows installer --------------------------------------------------------
function Skip($why) {
    if ($RequireInstaller) { throw $why }
    Write-Warning "$why; skipping the Windows installer."
}
$candidates = @($Iscc, (Get-Command iscc -ErrorAction SilentlyContinue)?.Source,
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
    "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") | Where-Object { $_ -and (Test-Path $_) }
$isccPath = $candidates | Select-Object -First 1
$wimlibExe = Join-Path $WimlibDir 'wimlib-imagex.exe'
if (-not $IsWindows) {
    Skip 'The installer is built on Windows'
} elseif (-not $isccPath) {
    Skip 'Inno Setup (ISCC.exe) not found: winget install JRSoftware.InnoSetup'
} elseif (-not (Test-Path $wimlibExe)) {
    Skip "wimlib not found in ${WimlibDir}: unzip wimlib-<version>-windows-x86_64-bin.zip from https://wimlib.net there"
} else {
    $wimlibVersion = ((& $wimlibExe --version | Select-Object -First 1) -replace '^wimlib-imagex\s+([0-9.]+).*$', '$1')
    $dist = (Resolve-Path dist).Path
    $wim = (Resolve-Path $WimlibDir).Path
    $isccArgs = @("/DAppVersion=$v", "/DFileVersion=$fileVersion", "/DDist=$dist", "/DWimlib=$wim",
        "/DWimlibVersion=$wimlibVersion", '/Q')
    if ($env:NETDEPLOY_SIGN) { $isccArgs += @('/DSign', "/Snetdeploy=$env:NETDEPLOY_SIGN") }
    & $isccPath @isccArgs packaging/windows/netdeploy.iss
    if ($LASTEXITCODE) { throw 'ISCC failed' }
}

Get-ChildItem dist -File | Where-Object { $_.Name -like 'netdeploy*.deb' -or $_.Name -like 'netdeploy-*-setup.exe' } |
    Format-Table Name, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
Write-Host 'Done.' -ForegroundColor Green
