#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
binary=${1:-$repo_root/dist/viberange-broker-linux-amd64}

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "error: run as root on the broker VM" >&2
  exit 1
fi
[[ -x $binary ]] || { echo "error: broker binary is missing or not executable: $binary" >&2; exit 1; }

if ! getent group viberange-broker >/dev/null; then
  groupadd --system viberange-broker
fi
if ! id viberange-broker >/dev/null 2>&1; then
  useradd --system \
    --gid viberange-broker \
    --home-dir /var/lib/viberange-broker \
    --shell /usr/sbin/nologin \
    viberange-broker
fi

install -d -o root -g viberange-broker -m 0750 /var/lib/viberange-broker
install -d -o root -g root -m 0700 /var/lib/viberange-broker/credentials
install -d -o viberange-broker -g viberange-broker -m 0700 /var/lib/viberange-broker/state
install -d -o root -g root -m 0755 /usr/local/libexec
install -d -o root -g root -m 0700 /var/backups/viberange-broker
install -o root -g root -m 0755 "$binary" /usr/local/bin/viberange-broker
install -o root -g root -m 0755 \
  "$repo_root/packaging/viberange-admin" \
  /usr/local/bin/vbr
install -o root -g root -m 0755 \
  "$repo_root/packaging/viberange-ludus-tunnel" \
  /usr/local/libexec/viberange-ludus-tunnel
install -o root -g root -m 0755 \
  "$repo_root/ops/backup/backup-broker.sh" \
  /usr/local/libexec/viberange-broker-backup
install -o root -g root -m 0644 \
  "$repo_root/packaging/systemd/viberange-broker.service" \
  /etc/systemd/system/viberange-broker.service
install -o root -g root -m 0644 \
  "$repo_root/packaging/systemd/viberange-ludus-tunnel.service" \
  /etc/systemd/system/viberange-ludus-tunnel.service
install -o root -g root -m 0644 \
  "$repo_root/packaging/systemd/viberange-broker-backup.service" \
  /etc/systemd/system/viberange-broker-backup.service
install -o root -g root -m 0644 \
  "$repo_root/packaging/systemd/viberange-broker-backup.timer" \
  /etc/systemd/system/viberange-broker-backup.timer

if [[ ! -e /var/lib/viberange-broker/broker.env ]]; then
  install -o root -g root -m 0640 \
    "$repo_root/config.example.env" \
    /var/lib/viberange-broker/broker.env
fi

systemctl daemon-reload

echo "Installed broker binaries, units, user, and persistent directory layout."
echo "Services remain disabled until credentials and production configuration are installed and validated."
