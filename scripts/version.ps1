#Requires -Version 7.2
<#
.SYNOPSIS
  The version for builds and packages (ADR 0031), from git tags: v0.6.0 gives
  0.6.0; three commits later 0.6.0-3-gabc1234; uncommitted changes add
  -dirty. Before the first tag: 0.0.0-<commits>-g<hash>. Returns Version,
  Deb (sorts correctly in dpkg: 0.6.0~dev3+gabc1234) and File (0.6.0.3).
#>
$described = (git describe --tags --always --dirty 2>$null)
if (-not $described) { return [pscustomobject]@{ Version = 'dev'; Deb = '0.0.0~dev'; File = '0.0.0.0' } }
$v = $described -replace '^v', ''
if ($v -match '^(\d+\.\d+\.\d+)(?:-(\d+)-g([0-9a-f]+))?(-dirty)?$') {
    $base, $n, $hash, $dirty = $Matches[1], $Matches[2], $Matches[3], $Matches[4]
} else {
    $base, $n = '0.0.0', (git rev-list --count HEAD)
    $hash = $v -replace '-dirty$', ''
    $dirty = $v.EndsWith('-dirty') ? '-dirty' : $null
    $v = "0.0.0-$n-g$hash$dirty"
}
$deb = $base
if ($n) { $deb += "~dev$n+g$hash" }
if ($dirty) { $deb += ($n ? '.dirty' : '+dirty') }
[pscustomobject]@{ Version = $v; Deb = $deb; File = "$base.$([int]($n ?? 0))" }
