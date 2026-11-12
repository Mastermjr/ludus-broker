package discord

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
)

var handoffSlotPattern = regexp.MustCompile(`^user[1-5]$`)

func validateHandoffMetadata(slot string, peer ludus.WireGuardPeer) error {
	if !handoffSlotPattern.MatchString(slot) || peer.UserID != slot {
		return fmt.Errorf("invalid WireGuard handoff slot metadata")
	}
	clientIP, _, err := net.ParseCIDR(peer.ClientIP)
	if err != nil || !strings.HasSuffix(peer.ClientIP, "/32") || clientIP == nil {
		return fmt.Errorf("invalid WireGuard client address metadata")
	}
	_, allowed, err := net.ParseCIDR(peer.AllowedRange)
	if err != nil || allowed == nil || !strings.HasSuffix(peer.AllowedRange, "/16") {
		return fmt.Errorf("invalid WireGuard allowed-range metadata")
	}
	return nil
}

func handoffOverview(slot string, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	values := strings.NewReplacer(
		"@@SLOT@@", slot,
		"@@CLIENT_IP@@", peer.ClientIP,
		"@@ALLOWED_RANGE@@", peer.AllowedRange,
		"@@SERVICE_IP@@", serviceIPForRange(peer.AllowedRange),
		"@@CALDERA_IP@@", rangeHostIP(peer.AllowedRange, 20, 20),
	)
	return []byte(values.Replace(handoffOverviewTemplate)), nil
}

func linuxHandoffGuide(slot string, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	values := strings.NewReplacer(
		"@@SLOT@@", slot,
		"@@CLIENT_IP@@", peer.ClientIP,
		"@@ALLOWED_RANGE@@", peer.AllowedRange,
		"@@SERVICE_IP@@", serviceIPForRange(peer.AllowedRange),
		"@@CALDERA_IP@@", rangeHostIP(peer.AllowedRange, 20, 20),
	)
	return []byte(values.Replace(linuxGuideTemplate)), nil
}

func linuxAcceptanceScript(slot string, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	values := strings.NewReplacer(
		"@@SLOT@@", slot,
		"@@CLIENT_IP@@", peer.ClientIP,
		"@@ALLOWED_RANGE@@", peer.AllowedRange,
		"@@SERVICE_IP@@", serviceIPForRange(peer.AllowedRange),
	)
	return []byte(values.Replace(linuxAcceptanceTemplate)), nil
}

func serviceIPForRange(cidr string) string {
	return rangeHostIP(cidr, 10, 10)
}

func rangeHostIP(cidr string, vlan, host byte) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil || len(ip) != net.IPv4len && ip.To4() == nil {
		return ""
	}
	value := ip.To4()
	return fmt.Sprintf("%d.%d.%d.%d", value[0], value[1], vlan, host)
}

const handoffOverviewTemplate = `# VibeRange WireGuard handoff

Assignment: **@@SLOT@@**<br>
Tunnel address: **@@CLIENT_IP@@**<br>
Assigned range: **@@ALLOWED_RANGE@@**

The accompanying ZIP contains your private, generation-bound WireGuard profile plus detailed Linux and Windows setup guides and acceptance scripts. Keep the ZIP and extracted **.conf** private; never paste the profile into Discord, tickets, logs, or screenshots.

## Start here

1. Download and extract ` + "`viberange-@@SLOT@@-handoff.zip`" + ` into a temporary directory.
2. Choose the matching platform instructions below.
3. Complete a handshake within ten minutes of assignment or the broker will automatically revoke the profile and reset the range.

### Linux

Install WireGuard tools, then run:

~~~bash
chmod 700 viberange-@@SLOT@@-acceptance.sh
./viberange-@@SLOT@@-acceptance.sh --background ./viberange-@@SLOT@@.conf
# Later:
./viberange-@@SLOT@@-acceptance.sh --cleanup
~~~

Use the script without ` + "`--background`" + ` for a bounded test that cleans up automatically. Read ` + "`viberange-@@SLOT@@-linux-setup.md`" + ` inside the ZIP for package installation, manual setup, and cleanup details.

### Windows 10/11

Install official WireGuard for Windows from https://www.wireguard.com/install/. Extract the ZIP, open **PowerShell as Administrator**, then run:

~~~powershell
Get-FileHash .\viberange-@@SLOT@@-acceptance.ps1 -Algorithm SHA256
Get-Content .\viberange-@@SLOT@@-acceptance.ps1
.\viberange-@@SLOT@@-acceptance.ps1 -ProfilePath .\viberange-@@SLOT@@.conf
~~~

Review the script first. If the machine's existing security policy blocks it, use the official WireGuard GUI instructions in the detailed Windows guide instead of changing security settings.

Read ` + "`viberange-@@SLOT@@-windows-setup.md`" + ` inside the ZIP for full installation, browser testing, ` + "`-KeepTunnel`" + `, and cleanup instructions.

## Workshop endpoints

- **CH-UI:** http://@@SERVICE_IP@@:3488
- **JupyterLab:** http://@@SERVICE_IP@@:8889
- **Caldera:** http://@@CALDERA_IP@@:8888

Service credentials are intentionally not embedded. When finished, use **/range leave** in Discord and wait for cleanup to complete. Do not reuse a profile after revocation or reset.
`

const linuxGuideTemplate = `# VibeRange Linux WireGuard setup

Assignment: **@@SLOT@@**  
Tunnel address: **@@CLIENT_IP@@**  
Assigned range: **@@ALLOWED_RANGE@@**

The .conf file in this archive is a private, generation-bound credential. Do not paste it into chat, tickets, logs, or screenshots. Normal acceptance mode cleans up automatically; ` + "`--background`" + ` deliberately leaves the tunnel and profile installed until a separate ` + "`--cleanup`" + ` invocation.

## 1. Install prerequisites

Debian or Ubuntu:

~~~bash
sudo apt update
sudo apt install wireguard-tools iproute2 curl netcat-openbsd
~~~

Fedora:

~~~bash
sudo dnf install wireguard-tools iproute curl nmap-ncat
~~~

NixOS temporary shell:

~~~bash
nix shell nixpkgs#wireguard-tools nixpkgs#iproute2 nixpkgs#curl nixpkgs#netcat
~~~

On NixOS, set networking.firewall.checkReversePath = "loose" in the machine's properly managed configuration. Do not rebuild from an incomplete /etc/nixos/configuration.nix.

## 2. Workshop endpoints

- **CH-UI:** http://@@SERVICE_IP@@:3488 — participant-facing evidence search and dashboards; directly allowed over this WireGuard assignment.
- **JupyterLab:** http://@@SERVICE_IP@@:8889 — participant notebooks and reporting; directly allowed over this WireGuard assignment. Use only the workshop-provided login/token; it is intentionally not embedded here.
- **Caldera:** http://@@CALDERA_IP@@:8888 — assigned-range attacker orchestration/API endpoint; directly allowed only for this WireGuard peer. Use only workshop-provided credentials, which are intentionally not embedded here.

## 3. Run the bounded acceptance test

Extract the Discord ZIP into a temporary directory, then run:

~~~bash
chmod 700 viberange-@@SLOT@@-acceptance.sh
./viberange-@@SLOT@@-acceptance.sh ./viberange-@@SLOT@@.conf
~~~

The script validates the profile, installs it mode 0600, starts WireGuard, checks the handshake and assigned routes, and tests the intended CH-UI, JupyterLab, and Caldera services. It no longer probes intentionally inaccessible networks.

To leave the validated tunnel running in the background:

~~~bash
./viberange-@@SLOT@@-acceptance.sh --background ./viberange-@@SLOT@@.conf
~~~

The WireGuard interface persists without a foreground script process. When finished, run cleanup separately from the same extracted directory:

~~~bash
./viberange-@@SLOT@@-acceptance.sh --cleanup
~~~

Cleanup stops the interface and removes the installed profile plus the canonical extracted profile. If the profile was renamed, pass its path after ` + "`--cleanup`" + `.

## 4. Manual lifecycle instead

~~~bash
sudo install -m 600 ./viberange-@@SLOT@@.conf /etc/wireguard/viberange-@@SLOT@@.conf
sudo wg-quick down viberange-@@SLOT@@ 2>/dev/null || true
sudo wg-quick up viberange-@@SLOT@@
ip -4 address show dev viberange-@@SLOT@@
ip route get @@SERVICE_IP@@
sudo wg show viberange-@@SLOT@@ latest-handshakes
sudo wg show viberange-@@SLOT@@ transfer
~~~

Expected address: @@CLIENT_IP@@. The profile must contain @@ALLOWED_RANGE@@ and must never contain a default route. An additional 198.51.100.1/32 gateway route is permitted when the workshop policy requires it.

Always tear down and remove the private file:

~~~bash
sudo wg-quick down viberange-@@SLOT@@
sudo rm -f /etc/wireguard/viberange-@@SLOT@@.conf ./viberange-@@SLOT@@.conf
~~~

Finally use /range leave in Discord and wait for the range to return through DRAINING and RESETTING to AVAILABLE.
`

const linuxAcceptanceTemplate = `#!/usr/bin/env bash
set -euo pipefail

slot='@@SLOT@@'
expected_client='@@CLIENT_IP@@'
expected_range='@@ALLOWED_RANGE@@'
service_ip='@@SERVICE_IP@@'
gateway='198.51.100.1/32'
interface="viberange-${slot}"
installed="/etc/wireguard/${interface}.conf"
canonical_source="./viberange-${slot}.conf"
mode='bounded'
profile=''
leave_running=false
source_profile=''

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }
usage() {
  printf 'Usage:\n  %s [--background] <profile>\n  %s --cleanup [profile]\n' "$0" "$0" >&2
  exit 2
}

while (($#)); do
  case "$1" in
    --background)
      [[ $mode == bounded ]] || usage
      mode='background'
      ;;
    --cleanup)
      [[ $mode == bounded ]] || usage
      mode='cleanup'
      ;;
    -h|--help) usage ;;
    --*) fail "unknown option: $1" ;;
    *)
      [[ -z $profile ]] || usage
      profile=$1
      ;;
  esac
  shift
done

[[ $interface =~ ^[a-zA-Z0-9_=+.-]{1,15}$ ]] || fail "unsafe WireGuard interface name"

cleanup() {
  set +e
  sudo wg-quick down "$interface" >/dev/null 2>&1
  sudo rm -f "$installed"
  if [[ -n $source_profile && $source_profile != "$installed" ]]; then
    rm -f -- "$source_profile"
  fi
}

if [[ $mode == cleanup ]]; then
  for command in wg-quick realpath sudo; do
    command -v "$command" >/dev/null || fail "missing prerequisite: $command"
  done
  source_profile=$(realpath -m "${profile:-$canonical_source}")
  cleanup
  pass "WireGuard tunnel and profile artifacts removed"
  exit 0
fi

[[ -n $profile && -f $profile ]] || usage
for command in wg wg-quick ip curl nc awk grep stat realpath sudo; do
  command -v "$command" >/dev/null || fail "missing prerequisite: $command"
done
source_profile=$(realpath "$profile")
trap 'if ! $leave_running; then cleanup; fi' EXIT
trap 'leave_running=false; exit 130' INT TERM HUP

sudo install -o root -g root -m 0600 "$source_profile" "$installed"
[[ $(sudo stat -c %a "$installed") == 600 ]] || fail "installed profile is not mode 0600"

grep -q '^PrivateKey = ' "$source_profile" || fail "profile has no private key"
address=$(awk -F' *= *' '$1=="Address" {print $2; exit}' "$source_profile")
[[ $address == "$expected_client" ]] || fail "unexpected client address"
allowed=$(awk -F' *= *' '$1=="AllowedIPs" {print $2; exit}' "$source_profile")
[[ -n $allowed ]] || fail "profile has no AllowedIPs"
normalized=${allowed// /}
range_seen=false
IFS=',' read -r -a entries <<<"$normalized"
for entry in "${entries[@]}"; do
  case "$entry" in
    "$expected_range") range_seen=true ;;
    "$gateway") ;;
    *) fail "unexpected AllowedIPs entry: $entry" ;;
  esac
done
$range_seen || fail "assigned range is absent from AllowedIPs"
[[ $normalized != *'0.0.0.0/0'* && $normalized != *'::/0'* ]] || fail "default route is forbidden"
pass "profile metadata and AllowedIPs validated"

sudo wg-quick down "$interface" >/dev/null 2>&1 || true
sudo wg-quick up "$interface"

actual_address=$(ip -o -4 address show dev "$interface" | awk '{print $4; exit}')
[[ $actual_address == "$expected_client" ]] || fail "live interface address mismatch"
ip route get "$service_ip" | grep -q "dev $interface" || fail "assigned service does not route through WireGuard"

handshake=0
for _ in $(seq 1 15); do
  curl --connect-timeout 2 -fsS "http://${service_ip}:3488/health" >/dev/null 2>&1 || true
  handshake=$(sudo wg show "$interface" latest-handshakes | awk 'NR==1 {print $2}')
  [[ ${handshake:-0} -gt 0 ]] && break
  sleep 2
done
[[ ${handshake:-0} -gt 0 ]] || fail "no WireGuard handshake"
pass "tunnel route and handshake"

curl --connect-timeout 5 -fsS "http://${service_ip}:3488/health" >/dev/null || fail "CH-UI health is unreachable"
nc -zvw5 "$service_ip" 8889 >/dev/null 2>&1 || fail "Jupyter TCP/8889 is unreachable"
range_prefix=${service_ip%.10.10}
nc -zvw5 "${range_prefix}.20.20" 8888 >/dev/null 2>&1 || fail "Caldera TCP/8888 is unreachable"
pass "assigned CH-UI, Jupyter, and Caldera services"
printf 'GO: assigned services are reachable.\n'

if [[ $mode == background ]]; then
  leave_running=true
  printf 'WireGuard remains active as %s. Run %s --cleanup when finished.\n' "$interface" "$0"
fi
`
