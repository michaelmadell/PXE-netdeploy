#Requires -Version 7.2
<#
.SYNOPSIS
  Runs netdeployd and the Vite dev server together. Ctrl+C stops both.
.NOTES
  The server is built to .tmp/ and run directly (not via `go run`) so that
  stopping it doesn't leave an orphaned child process on Windows.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$exe = Join-Path '.tmp' ($IsWindows ? 'netdeployd.exe' : 'netdeployd')
go build -o $exe ./cmd/netdeployd
if ($LASTEXITCODE) { throw 'go build failed' }

$server = Start-Process -FilePath $exe -ArgumentList '-data', './data', '-admin-addr', '0.0.0.0:8443' -NoNewWindow -PassThru
try {
    Write-Host 'Admin API: https://127.0.0.1:8443  |  UI (dev): http://localhost:5173' -ForegroundColor Cyan
    pnpm --dir web dev
}
finally {
    if (-not $server.HasExited) { Stop-Process -Id $server.Id -Force }
}
