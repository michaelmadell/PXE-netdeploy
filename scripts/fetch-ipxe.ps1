#Requires -Version 7.2
<#
.SYNOPSIS
  Downloads the signed iPXE chain for HTTP(S) Boot (ADR 0032) into
  ./dist/ipxe/<arch>/, checking every file against a pinned SHA-256.
.DESCRIPTION
  ipxe-shim.efi  ipxe/shim ipxe-16.1 (Microsoft UEFI CA 2011 and 2023)
  ipxe.efi       iPXE v2.0.0 ipxeboot.tar.gz, <arch>-sb/snponly.efi (iPXE Secure Boot CA),
                 saved as ipxe.efi because the shim loads that name (ADR 0032, amended
                 2026-11-23: the full ipxe.efi's USB drivers break some keyboards)
  wimboot        wimboot v2.9.0 (Microsoft UEFI CA 2011 and 2023)
  Plus the licences and packaging/ipxe-SOURCE.txt. netdeployd finds
  dist/ipxe in a checkout; the packages install the same tree. To update,
  change the versions and hashes here and in the ADR.
.PARAMETER Out
  Where to put the tree (default dist/ipxe).
#>
param([string]$Out = 'dist/ipxe')
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$shim = 'https://github.com/ipxe/shim/releases/download/ipxe-16.1'
$ipxe = 'https://github.com/ipxe/ipxe/releases/download/v2.0.0/ipxeboot.tar.gz'
$ipxeSha = '01a526d4cc791fc30362259c609d6c506cc64a7bdff51b9a5eb788354e17eee1'
$wimboot = 'https://github.com/ipxe/wimboot/releases/download/v2.9.0'
$pins = @{
    'x64/ipxe-shim.efi'   = '5eecca2780bd49c900565e124516a1bd666ec5e012825f34991b6ba1ef2fa6cf'
    'arm64/ipxe-shim.efi' = '31c6d8ef9ed24dc810dd7b951cf86b2e9036a02dea8a32548c0e1c0ae4d6f1c9'
    'x64/ipxe.efi'        = 'b1e67c3e4a1e8708ddfd0079ad4505e3a02245acb55ee9a95437ab3c507be82a'
    'arm64/ipxe.efi'      = 'bed8da8639b45eeec55d41627349863efa5085a2113bb1193838b8a463e5f49e'
    'x64/wimboot'         = '5f067ccdc4d084d5bf77b6c853bd0f8402dfc2b4cd1b103d358993ae97fae8e3'
    'arm64/wimboot'       = 'b1440c6386981fb447b2f341294ec7a59546d496dff0f075b0f698c853f1c949'
}
$licences = @{
    'iPXE-COPYING.GPLv2' = 'https://raw.githubusercontent.com/ipxe/ipxe/v2.0.0/COPYING.GPLv2'
    'iPXE-COPYING.UBDL'  = 'https://raw.githubusercontent.com/ipxe/ipxe/v2.0.0/COPYING.UBDL'
    'shim-COPYRIGHT'     = 'https://raw.githubusercontent.com/ipxe/shim/ipxe-16.1/COPYRIGHT'
    'wimboot-LICENSE'    = 'https://raw.githubusercontent.com/ipxe/wimboot/v2.9.0/LICENSE.txt'
}

function Sha($path) { (Get-FileHash $path -Algorithm SHA256).Hash.ToLower() }
function Fetch($url, $path) {
    New-Item -ItemType Directory -Force (Split-Path $path) | Out-Null
    Invoke-WebRequest $url -OutFile $path
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) "netdeploy-ipxe-$PID"
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
    Fetch "$shim/ipxe-shimx64.efi" "$Out/x64/ipxe-shim.efi"
    Fetch "$shim/ipxe-shimaa64.efi" "$Out/arm64/ipxe-shim.efi"
    Fetch "$wimboot/wimboot" "$Out/x64/wimboot"
    Fetch "$wimboot/wimboot.arm64" "$Out/arm64/wimboot"

    Fetch $ipxe "$tmp/ipxeboot.tar.gz"
    if ((Sha "$tmp/ipxeboot.tar.gz") -ne $ipxeSha) { throw 'ipxeboot.tar.gz: SHA-256 mismatch' }
    # Only the two files: the archive's symlinks don't extract on Windows.
    tar -xzf "$tmp/ipxeboot.tar.gz" -C $tmp ipxeboot/x86_64-sb/snponly.efi ipxeboot/arm64-sb/snponly.efi
    if ($LASTEXITCODE) { throw 'tar failed' }
    # snponly.efi under the name the iPXE shim loads.
    Copy-Item "$tmp/ipxeboot/x86_64-sb/snponly.efi" "$Out/x64/ipxe.efi" -Force
    Copy-Item "$tmp/ipxeboot/arm64-sb/snponly.efi" "$Out/arm64/ipxe.efi" -Force

    foreach ($f in $pins.Keys) {
        if ((Sha "$Out/$f") -ne $pins[$f]) { throw "${f}: SHA-256 mismatch" }
    }
    foreach ($l in $licences.Keys) { Fetch $licences[$l] "$Out/licences/$l.txt" }
    Copy-Item packaging/ipxe-SOURCE.txt "$Out/SOURCE.txt" -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
Write-Host "Signed iPXE chain in $Out (hashes checked)." -ForegroundColor Green
