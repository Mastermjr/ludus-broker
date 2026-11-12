#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: sudo $0 <plugin.so> [ludus-install-path]" >&2
  exit 2
}

[[ $# -ge 1 && $# -le 2 ]] || usage
[[ ${EUID:-$(id -u)} -eq 0 ]] || { echo "installation must run as root" >&2; exit 1; }
plugin=$(realpath "$1")
install_root=${2:-/opt/ludus}
destination="$install_root/plugins/community/admin/viberange-wireguard.so"

[[ -f "$plugin" ]] || { echo "plugin does not exist: $plugin" >&2; exit 1; }
install -d -o root -g root -m 0755 "$(dirname "$destination")"
install -o root -g root -m 0644 "$plugin" "$destination"
install -d -o root -g root -m 0700 /var/lib/ludus/viberange-wireguard

echo "installed $destination"
echo "restart the ludus-admin service, then verify the authenticated status endpoint"
