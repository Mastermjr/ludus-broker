package discord

import (
	"strings"

	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
)

func windowsHandoffGuide(slot string, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	values := windowsHandoffValues(slot, peer)
	return []byte(values.Replace(windowsGuideTemplate)), nil
}

func windowsAcceptanceScript(slot string, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	values := windowsHandoffValues(slot, peer)
	return []byte(values.Replace(windowsAcceptanceTemplate)), nil
}

func windowsHandoffValues(slot string, peer ludus.WireGuardPeer) *strings.Replacer {
	return strings.NewReplacer(
		"@@SLOT@@", slot,
		"@@CLIENT_IP@@", peer.ClientIP,
		"@@CLIENT_ADDRESS@@", strings.SplitN(peer.ClientIP, "/", 2)[0],
		"@@ALLOWED_RANGE@@", peer.AllowedRange,
		"@@SERVICE_IP@@", serviceIPForRange(peer.AllowedRange),
		"@@CALDERA_IP@@", rangeHostIP(peer.AllowedRange, 20, 20),
		"@@DENIED_IP@@", rangeHostIP(peer.AllowedRange, 30, 30),
	)
}

const windowsGuideTemplate = `# VibeRange Windows WireGuard setup

Assignment: **@@SLOT@@**  
Tunnel address: **@@CLIENT_IP@@**  
Assigned range: **@@ALLOWED_RANGE@@**

The **.conf** file in this archive is a private, generation-bound credential. Do not paste it into chat, tickets, logs, screenshots, PowerShell history, or browser forms. The acceptance script removes the extracted profile when it exits. By default it also disconnects WireGuard and removes the protected installed copy.

## 1. Install WireGuard

Use a supported 64-bit Windows 10 or Windows 11 system with local administrator access and PowerShell 5.1 or newer. Download and install the official WireGuard for Windows package only from:

~~~text
https://www.wireguard.com/install/
~~~

Confirm these files exist:

~~~powershell
Test-Path "$env:ProgramFiles\WireGuard\wireguard.exe"
Test-Path "$env:ProgramFiles\WireGuard\wg.exe"
~~~

Both commands must return **True**. Disable other overlay VPNs during the bounded isolation test. A disposable VM is preferred.

## 2. Extract and run the acceptance script

Extract the ZIP into a temporary directory. Open **PowerShell as Administrator**, change to that directory, and run:

~~~powershell
Set-ExecutionPolicy -Scope Process Bypass
Unblock-File .\viberange-@@SLOT@@-acceptance.ps1
.\viberange-@@SLOT@@-acceptance.ps1 -ProfilePath .\viberange-@@SLOT@@.conf
~~~

The script validates the assigned address and routes, protects the installed profile with an Administrators/SYSTEM-only ACL, starts WireGuard, checks the handshake, tests the assigned CH-UI, JupyterLab, and Caldera endpoints, verifies selected unapproved services are denied, then disconnects and removes both profile copies.

To keep the tunnel connected for browser testing:

~~~powershell
.\viberange-@@SLOT@@-acceptance.ps1 -ProfilePath .\viberange-@@SLOT@@.conf -KeepTunnel
~~~

## 3. Workshop endpoints

- **CH-UI:** http://@@SERVICE_IP@@:3488
- **JupyterLab:** http://@@SERVICE_IP@@:8889
- **Caldera:** http://@@CALDERA_IP@@:8888

Credentials and service tokens are intentionally not embedded in this archive.

## 4. Troubleshooting

A running WireGuard tunnel service proves only that the local interface is enabled. The acceptance script also requires a handshake no more than 180 seconds old and successful assigned-service traffic. If it reports that no recent handshake exists, the profile may have been revoked, expired, reset, or removed from the live gateway; request a fresh assignment rather than repeatedly reinstalling the stale profile.

The script treats the actual Windows service state as authoritative because the WireGuard installer process can occasionally return a nonzero exit code even after installing a running tunnel service. Failed validation always removes the service and protected profile, even when **-KeepTunnel** was requested.

## 5. Disconnect and clean up

If **-KeepTunnel** was used, run from an elevated PowerShell window:

~~~powershell
& "$env:ProgramFiles\WireGuard\wireguard.exe" /uninstalltunnelservice viberange-@@SLOT@@
Remove-Item -Force "$env:ProgramData\VibeRange\viberange-@@SLOT@@.conf"
~~~

Then use **/range leave** in Discord and wait for the range to return to **AVAILABLE**. Never reuse a profile after broker-side revocation or range reset.
`

const windowsAcceptanceTemplate = `[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$ProfilePath,
    [switch]$KeepTunnel
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Slot = '@@SLOT@@'
$Interface = "viberange-$Slot"
$ExpectedClient = '@@CLIENT_IP@@'
$ExpectedClientAddress = '@@CLIENT_ADDRESS@@'
$ExpectedRange = '@@ALLOWED_RANGE@@'
$Gateway = '198.51.100.1/32'
$ServiceIp = '@@SERVICE_IP@@'
$CalderaIp = '@@CALDERA_IP@@'
$DeniedIp = '@@DENIED_IP@@'
$WireGuardExe = Join-Path $env:ProgramFiles 'WireGuard\wireguard.exe'
$WgExe = Join-Path $env:ProgramFiles 'WireGuard\wg.exe'
$InstallDirectory = Join-Path $env:ProgramData 'VibeRange'
$InstalledProfile = Join-Path $InstallDirectory "$Interface.conf"
$TunnelService = 'WireGuardTunnel$' + $Interface
$Started = $false
$Validated = $false
$SourceProfile = $null

function Write-Pass([string]$Message) { Write-Host "PASS: $Message" }
function Stop-Failure([string]$Message) { throw "FAIL: $Message" }

function Test-TcpPort {
    param([Parameter(Mandatory)][string]$TargetHost, [Parameter(Mandatory)][int]$Port, [int]$TimeoutMilliseconds = 3000)
    $client = [Net.Sockets.TcpClient]::new()
    try {
        $attempt = $client.BeginConnect($TargetHost, $Port, $null, $null)
        if (-not $attempt.AsyncWaitHandle.WaitOne($TimeoutMilliseconds, $false)) { return $false }
        $client.EndConnect($attempt)
        return $true
    }
    catch { return $false }
    finally { $client.Dispose() }
}

function Assert-Denied {
    param([Parameter(Mandatory)][string]$TargetHost, [Parameter(Mandatory)][int]$Port)
    if (Test-TcpPort -TargetHost $TargetHost -Port $Port) {
        Stop-Failure "unapproved assigned-range service reachable: ${TargetHost}:$Port"
    }
}

function Assert-Http {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Uri)
    try {
        $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 8 -Uri $Uri
        if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 400) {
            Stop-Failure "$Name returned HTTP $($response.StatusCode)"
        }
    }
    catch { Stop-Failure "$Name is unreachable" }
}

function Remove-TunnelService {
    if (-not (Get-Service -Name $TunnelService -ErrorAction SilentlyContinue)) { return }
    $null = Start-Process -FilePath $WireGuardExe -Wait -PassThru -WindowStyle Hidden -ArgumentList @('/uninstalltunnelservice', $Interface)
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        if (-not (Get-Service -Name $TunnelService -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 500
    }
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Stop-Failure 'run this script from an elevated PowerShell window'
}

try {
    if (-not (Test-Path -LiteralPath $ProfilePath -PathType Leaf)) { Stop-Failure "profile not found: $ProfilePath" }
    $SourceProfile = (Resolve-Path -LiteralPath $ProfilePath).Path
    if (-not (Test-Path $WireGuardExe) -or -not (Test-Path $WgExe)) { Stop-Failure 'WireGuard for Windows is not installed' }

    $profileLines = Get-Content -LiteralPath $SourceProfile
    if (-not ($profileLines | Where-Object { $_ -match '^PrivateKey\s*=\s*\S+' })) { Stop-Failure 'profile has no private key' }
    $addressLine = $profileLines | Where-Object { $_ -match '^Address\s*=' } | Select-Object -First 1
    if (-not $addressLine) { Stop-Failure 'profile has no Address' }
    if ((($addressLine -split '=', 2)[1].Trim()) -ne $ExpectedClient) { Stop-Failure 'unexpected client address' }
    $allowedLine = $profileLines | Where-Object { $_ -match '^AllowedIPs\s*=' } | Select-Object -First 1
    if (-not $allowedLine) { Stop-Failure 'profile has no AllowedIPs' }
    $allowedEntries = @((($allowedLine -split '=', 2)[1] -replace '\s', '') -split ',')
    if ($allowedEntries -notcontains $ExpectedRange) { Stop-Failure 'assigned range is absent from AllowedIPs' }
    foreach ($entry in $allowedEntries) {
        if ($entry -ne $ExpectedRange -and $entry -ne $Gateway) { Stop-Failure "unexpected AllowedIPs entry: $entry" }
    }
    if ($allowedEntries -contains '0.0.0.0/0' -or $allowedEntries -contains '::/0') { Stop-Failure 'default route is forbidden' }
    Write-Pass 'profile metadata and AllowedIPs validated'

    Remove-TunnelService
    New-Item -ItemType Directory -Force $InstallDirectory | Out-Null
    if ($SourceProfile -ne $InstalledProfile) {
        Copy-Item -LiteralPath $SourceProfile -Destination $InstalledProfile -Force
    }
    & icacls.exe $InstalledProfile /inheritance:r /grant:r '*S-1-5-32-544:(F)' '*S-1-5-18:(F)' | Out-Null
    if ($LASTEXITCODE -ne 0) { Stop-Failure 'could not protect the installed profile ACL' }

    $install = Start-Process -FilePath $WireGuardExe -Wait -PassThru -WindowStyle Hidden -ArgumentList @('/installtunnelservice', $InstalledProfile)
    $service = $null
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        $service = Get-Service -Name $TunnelService -ErrorAction SilentlyContinue
        if ($service) { $Started = $true }
        if ($service -and $service.Status -eq 'Running') { break }
        Start-Sleep -Seconds 1
    }
    if (-not $service -or $service.Status -ne 'Running') {
        Stop-Failure "WireGuard tunnel service did not start (installer exit $($install.ExitCode))"
    }

    $liveAddress = Get-NetIPAddress -AddressFamily IPv4 -InterfaceAlias $Interface -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -eq $ExpectedClientAddress }
    if (-not $liveAddress) { Stop-Failure 'live interface address mismatch' }
    $serviceRoute = Find-NetRoute -RemoteIPAddress $ServiceIp -ErrorAction SilentlyContinue
    if (-not ($serviceRoute | Where-Object { $_.InterfaceAlias -eq $Interface })) { Stop-Failure 'assigned service does not route through WireGuard' }

    $handshake = 0L
    $handshakeAge = [int64]::MaxValue
    for ($attempt = 0; $attempt -lt 15; $attempt++) {
        try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://${ServiceIp}:3488/health" | Out-Null } catch {}
        $handshakeLine = & $WgExe show $Interface latest-handshakes | Select-Object -First 1
        if ($handshakeLine) {
            $handshake = [int64](($handshakeLine -split '\s+')[-1])
            if ($handshake -gt 0) {
                $handshakeAge = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() - $handshake
            }
        }
        if ($handshakeAge -ge 0 -and $handshakeAge -le 180) { break }
        Start-Sleep -Seconds 2
    }
    if ($handshakeAge -lt 0 -or $handshakeAge -gt 180) {
        Stop-Failure 'no recent WireGuard handshake; the profile may be revoked, expired, or absent from the live gateway'
    }
    Write-Pass 'tunnel address, route, and recent handshake'

    Assert-Http -Name 'CH-UI health' -Uri "http://${ServiceIp}:3488/health"
    if (-not (Test-TcpPort -TargetHost $ServiceIp -Port 8889 -TimeoutMilliseconds 5000)) { Stop-Failure 'Jupyter TCP/8889 is unreachable' }
    if (-not (Test-TcpPort -TargetHost $CalderaIp -Port 8888 -TimeoutMilliseconds 5000)) { Stop-Failure 'Caldera TCP/8888 is unreachable' }
    Write-Pass 'assigned CH-UI, Jupyter, and Caldera services'

    foreach ($probe in @(@($ServiceIp, 22), @($CalderaIp, 22), @($DeniedIp, 22), @($DeniedIp, 1230))) {
        Assert-Denied -TargetHost $probe[0] -Port $probe[1]
    }
    Write-Pass 'unapproved assigned-range services denied'
    $Validated = $true
    Write-Host 'GO: assigned services passed and unapproved assigned-range probes remained blocked.'
}
finally {
    $leaveConnected = $KeepTunnel -and $Validated
    if ($Started -and -not $leaveConnected) { Remove-TunnelService }
    if (-not $leaveConnected) { Remove-Item -LiteralPath $InstalledProfile -Force -ErrorAction SilentlyContinue }
    if ($SourceProfile -and $SourceProfile -ne $InstalledProfile) { Remove-Item -LiteralPath $SourceProfile -Force -ErrorAction SilentlyContinue }
}

if ($KeepTunnel) {
    Write-Host "WireGuard remains connected as $Interface."
    Write-Host "Disconnect later with: & '$WireGuardExe' /uninstalltunnelservice $Interface"
    Write-Host "Then remove: $InstalledProfile"
}
`
