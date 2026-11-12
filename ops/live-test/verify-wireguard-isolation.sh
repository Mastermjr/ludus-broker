#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: sudo verify-wireguard-isolation.sh PROFILE EXPECTED_RANGE_CIDR \
  --allow-url URL [--allow-url URL ...] \
  --deny HOST:PORT [--deny HOST:PORT ...]

Run only on an approved disposable test client. The script never prints the
profile or private key and always tears the interface down.
EOF
  exit 2
}

[[ $# -ge 4 ]] || usage
[[ ${EUID:-$(id -u)} -eq 0 ]] || { echo "error: run as root on a disposable test client" >&2; exit 1; }
profile=$(realpath "$1")
expected_cidr=$2
shift 2
[[ -f $profile ]] || { echo "error: profile not found" >&2; exit 1; }
[[ $(stat -c %a "$profile") == 600 ]] || { echo "error: profile must be mode 0600" >&2; exit 1; }

allow_urls=()
deny_targets=()
while (($#)); do
  case $1 in
    --allow-url) [[ $# -ge 2 ]] || usage; allow_urls+=("$2"); shift 2 ;;
    --deny) [[ $# -ge 2 ]] || usage; deny_targets+=("$2"); shift 2 ;;
    *) usage ;;
  esac
done
((${#allow_urls[@]} > 0 && ${#deny_targets[@]} > 0)) || usage

allowed_ips=$(awk -F= '/^[[:space:]]*AllowedIPs[[:space:]]*=/{gsub(/[[:space:]]/, "", $2); print $2}' "$profile")
[[ $allowed_ips == "$expected_cidr" ]] || { echo "error: profile routes are not restricted to the expected range CIDR" >&2; exit 1; }
client_ip=$(awk -F= '/^[[:space:]]*Address[[:space:]]*=/{gsub(/[[:space:]]/, "", $2); sub(/\/.*/, "", $2); print $2}' "$profile")
[[ -n $client_ip ]] || { echo "error: profile omits client address" >&2; exit 1; }

interface=$(basename "$profile" .conf)
((${#interface} <= 15)) || { echo "error: profile basename must be at most 15 characters" >&2; exit 1; }
cleanup() { wg-quick down "$profile" >/dev/null 2>&1 || true; }
trap cleanup EXIT
wg-quick up "$profile" >/dev/null

for url in "${allow_urls[@]}"; do
  curl --fail --silent --show-error --max-time 10 --interface "$interface" -o /dev/null "$url"
done

for target in "${deny_targets[@]}"; do
  host=${target%:*}
  port=${target##*:}
  if timeout 4 nc -s "$client_ip" -z "$host" "$port" >/dev/null 2>&1; then
    echo "error: forbidden target was reachable: $target" >&2
    exit 1
  fi
done

handshake=$(wg show "$interface" latest-handshakes | awk '{print $2}' | sort -nr | head -1)
[[ ${handshake:-0} -gt 0 ]] || { echo "error: no WireGuard handshake observed" >&2; exit 1; }
echo "WireGuard connectivity and isolation checks passed"
