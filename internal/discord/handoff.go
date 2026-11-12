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

const linuxGuideTemplate = `# VibeRange Linux WireGuard setup

Assignment: **@@SLOT@@**  
Tunnel address: **@@CLIENT_IP@@**  
Assigned range: **@@ALLOWED_RANGE@@**

The .conf file in this archive is a private, generation-bound credential. Do not paste it into chat, tickets, logs, or screenshots. The acceptance script removes the installed and extracted profile when it exits.

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

The script validates the profile, installs it mode 0600, starts WireGuard, checks the handshake and assigned routes, tests CH-UI and Jupyter connectivity, probes denied services, adversarially expands client routes to verify server-side isolation, restores the peer configuration, tears down the interface, and removes the private profile.

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
profile=${1:-}
interface="viberange-${slot}"
installed="/etc/wireguard/${interface}.conf"
peer=''
old_allowed=''
started=false
source_profile=''
added_routes=()

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

cleanup() {
  set +e
  if [[ -n $peer && -n $old_allowed ]]; then
    sudo wg set "$interface" peer "$peer" allowed-ips "$old_allowed" >/dev/null 2>&1
  fi
  for route in "${added_routes[@]}"; do
    sudo ip route del "$route" dev "$interface" >/dev/null 2>&1
  done
  if $started; then
    sudo wg-quick down "$interface" >/dev/null 2>&1
  fi
  sudo rm -f "$installed"
  if [[ -n $source_profile && $source_profile != "$installed" ]]; then
    rm -f -- "$source_profile"
  fi
}
trap cleanup EXIT INT TERM HUP

for command in wg wg-quick ip curl nc awk grep stat realpath sudo; do
  command -v "$command" >/dev/null || fail "missing prerequisite: $command"
done
[[ -n $profile && -f $profile ]] || fail "usage: $0 <viberange-${slot}.conf>"
source_profile=$(realpath "$profile")
[[ $interface =~ ^[a-zA-Z0-9_=+.-]{1,15}$ ]] || fail "unsafe WireGuard interface name"

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
started=true

actual_address=$(ip -o -4 address show dev "$interface" | awk '{print $4; exit}')
[[ $actual_address == "$expected_client" ]] || fail "live interface address mismatch"
ip route get "$service_ip" | grep -q "dev $interface" || fail "assigned service does not route through WireGuard"

handshake=0
for _ in $(seq 1 15); do
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

for target_port in \
  "${service_ip}:22" \
  "${range_prefix}.20.20:22" \
  "${range_prefix}.30.30:22" \
  "${range_prefix}.30.30:1230"
do
  host=${target_port%:*}; port=${target_port##*:}
  if nc -zvw3 "$host" "$port" >/dev/null 2>&1; then
    fail "unapproved assigned-range service reachable: $target_port"
  fi
done
pass "unapproved assigned-range services denied"

peer=$(sudo wg show "$interface" peers | head -1)
[[ -n $peer ]] || fail "server peer is missing"
old_allowed=$(sudo wg show "$interface" allowed-ips | awk -v peer="$peer" '$1==peer {$1=""; sub(/^[ \t]+/,""); print}')
[[ -n $old_allowed ]] || fail "could not save original peer AllowedIPs"

adversarial=(198.51.100.1/32 10.6.0.0/16 10.7.0.0/16 10.10.0.0/16 10.11.66.0/24 10.11.100.0/24 10.12.0.0/16 10.13.0.0/16)
expanded=$old_allowed
for route in "${adversarial[@]}"; do
  [[ ,$expanded, == *,$route,* ]] || expanded+=",$route"
done
sudo wg set "$interface" peer "$peer" allowed-ips "$expanded"
for route in "${adversarial[@]}"; do
  [[ $route == "$expected_range" ]] && continue
  sudo ip route replace "$route" dev "$interface"
  added_routes+=("$route")
done

for target in 10.6.10.10 10.7.10.10 10.10.10.10 10.12.10.10 10.13.10.10; do
  [[ $target == "$service_ip" ]] && continue
  for port in 22 3488 8889; do
    if nc -zvw3 "$target" "$port" >/dev/null 2>&1; then
      fail "cross-range service reachable: $target:$port"
    fi
  done
done
for host in 198.51.100.1 10.11.66.100 10.11.100.100; do
  for port in 22 3128 8006 8080 8081; do
    if nc -zvw3 "$host" "$port" >/dev/null 2>&1; then
      fail "management service reachable: $host:$port"
    fi
  done
done
pass "cross-range and management services denied after adversarial route expansion"
printf 'GO: assigned services passed and every denied probe remained blocked.\n'
`
