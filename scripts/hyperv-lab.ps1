#Requires -Version 7.2
#Requires -RunAsAdministrator
<#
.SYNOPSIS
  Creates (or removes) a Hyper-V test lab for network boot: an internal switch
  with a static host IP and no DHCP, plus a Gen 2 VM with Secure Boot and a
  vTPM that boots from the network first.

.DESCRIPTION
  The host gets a vEthernet adapter on the lab switch; run netdeployd with
  `full` DHCP on that interface. Nothing else hands out addresses on the switch,
  so every DHCP answer the VM sees comes from netdeployd.

  -SecureBootTemplate picks what the VM's firmware trusts:
    MicrosoftWindows                    Windows Production PCA 2011 (Windows boot managers)
    MicrosoftUEFICertificateAuthority   Microsoft UEFI CA 2011 only (shim, iPXE). This
                                        REPLACES the Windows CA, so no Windows boot
                                        manager boots on it. Real PCs usually trust both;
                                        no Hyper-V template does.

.EXAMPLE
  ./scripts/hyperv-lab.ps1
.EXAMPLE
  ./scripts/hyperv-lab.ps1 -VMName nd-shim -SecureBootTemplate MicrosoftUEFICertificateAuthority
.EXAMPLE
  ./scripts/hyperv-lab.ps1 -Remove
#>
[CmdletBinding()]
param(
    [string]$VMName = 'nd-client-01',
    [string]$SwitchName = 'NetDeployLab',
    [string]$HostIp = '10.77.0.1',
    [int]$PrefixLength = 24,
    [ValidateSet('MicrosoftWindows', 'MicrosoftUEFICertificateAuthority')]
    [string]$SecureBootTemplate = 'MicrosoftWindows',
    [int]$MemoryGB = 4,
    [int]$DiskGB = 80,
    [switch]$Remove
)
$ErrorActionPreference = 'Stop'

if (-not $IsWindows) { throw 'The Hyper-V lab only runs on Windows.' }
if (-not (Get-Command New-VM -ErrorAction SilentlyContinue)) {
    throw 'Hyper-V PowerShell module not found. Enable the Hyper-V feature (including management tools) first.'
}

$adapterAlias = "vEthernet ($SwitchName)"
$vhdDir = Join-Path (Get-VMHost).VirtualHardDiskPath 'netdeploy'
$vhdPath = Join-Path $vhdDir "$VMName.vhdx"

if ($Remove) {
    if (Get-VM -Name $VMName -ErrorAction SilentlyContinue) {
        Stop-VM -Name $VMName -TurnOff -Force -ErrorAction SilentlyContinue
        Remove-VM -Name $VMName -Force
        Write-Host "Removed VM $VMName"
    }
    if (Test-Path $vhdPath) { Remove-Item $vhdPath -Force; Write-Host "Removed $vhdPath" }
    Write-Host "Switch '$SwitchName' and firewall rules were kept; remove them with:"
    Write-Host "  Remove-VMSwitch -Name '$SwitchName'; Remove-NetFirewallRule -Group 'netdeploy lab'"
    return
}

# --- Lab switch with a static host address -------------------------------------------
if (-not (Get-VMSwitch -Name $SwitchName -ErrorAction SilentlyContinue)) {
    New-VMSwitch -Name $SwitchName -SwitchType Internal | Out-Null
    Write-Host "Created internal switch $SwitchName"
}
$existing = Get-NetIPAddress -InterfaceAlias $adapterAlias -AddressFamily IPv4 -ErrorAction SilentlyContinue
if (-not ($existing | Where-Object IPAddress -eq $HostIp)) {
    $existing | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
    New-NetIPAddress -InterfaceAlias $adapterAlias -IPAddress $HostIp -PrefixLength $PrefixLength | Out-Null
    Write-Host "Host address on '$adapterAlias': $HostIp/$PrefixLength"
}

# --- Firewall: DHCP, proxyDHCP, TFTP, HTTP(S) on the lab adapter only --------------
if (-not (Get-NetFirewallRule -Group 'netdeploy lab' -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -DisplayName 'netdeploy lab UDP' -Group 'netdeploy lab' -Direction Inbound `
        -Protocol UDP -LocalPort 67, 69, 4011 -InterfaceAlias $adapterAlias -Action Allow | Out-Null
    New-NetFirewallRule -DisplayName 'netdeploy lab TCP' -Group 'netdeploy lab' -Direction Inbound `
        -Protocol TCP -LocalPort 80, 443 -InterfaceAlias $adapterAlias -Action Allow | Out-Null
    Write-Host 'Added firewall rules (group: netdeploy lab)'
}

# --- VM ------------------------------------------------------------------------------
if (Get-VM -Name $VMName -ErrorAction SilentlyContinue) {
    throw "VM $VMName already exists. Use -Remove first, or pick another -VMName."
}
New-Item -ItemType Directory -Force $vhdDir | Out-Null

New-VM -Name $VMName -Generation 2 -MemoryStartupBytes ($MemoryGB * 1GB) `
    -NewVHDPath $vhdPath -NewVHDSizeBytes ($DiskGB * 1GB) -SwitchName $SwitchName | Out-Null

# Static memory: WinPE loads boot.wim into a RAM disk.
Set-VMMemory -VMName $VMName -DynamicMemoryEnabled $false
Set-VMProcessor -VMName $VMName -Count 2

# Windows 11 requires TPM 2.0.
Set-VMKeyProtector -VMName $VMName -NewLocalKeyProtector
Enable-VMTPM -VMName $VMName

$nic = Get-VMNetworkAdapter -VMName $VMName
Set-VMFirmware -VMName $VMName -EnableSecureBoot On -SecureBootTemplate $SecureBootTemplate -FirstBootDevice $nic

Write-Host ''
Write-Host "Created $VMName (Gen 2, Secure Boot: $SecureBootTemplate, network boot first)." -ForegroundColor Green
Write-Host "MAC address: $((Get-VMNetworkAdapter -VMName $VMName).MacAddress)"
Write-Host "Configure netdeployd with interface '$adapterAlias' in 'full' DHCP mode, then: Start-VM $VMName"
