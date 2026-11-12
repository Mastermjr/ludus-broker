#!/usr/bin/env bash
set -euo pipefail

backup=${1:?usage: restore-test.sh <backup.db.age> [recovered.db]}
recovered=${2:-}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
manifest=${backup%.db.age}.manifest

for command in age sqlite3 sha256sum; do
  command -v "$command" >/dev/null || { echo "error: required command not found: $command" >&2; exit 1; }
done
[[ -s $backup ]] || { echo "error: encrypted backup is missing: $backup" >&2; exit 1; }
[[ -s $manifest ]] || { echo "error: backup manifest is missing: $manifest" >&2; exit 1; }
[[ -s $age_identity ]] || { echo "error: age identity is missing: $age_identity" >&2; exit 1; }

expected=$(awk -F= '$1=="sha256"{print $2}' "$manifest")
actual=$(sha256sum "$backup" | awk '{print $1}')
[[ -n $expected && $expected == "$actual" ]] || {
  echo "error: encrypted backup checksum mismatch" >&2
  exit 1
}

umask 077
tmp=$(mktemp -d /dev/shm/viberange-restore.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
age --decrypt --identity "$age_identity" --output "$tmp/recovered.db" "$backup"
[[ $(sqlite3 "$tmp/recovered.db" 'PRAGMA integrity_check;') == ok ]] || {
  echo "error: restored SQLite integrity check failed" >&2
  exit 1
}

counts=$(sqlite3 -json "$tmp/recovered.db" \
  "SELECT 'slots' AS table_name, count(*) AS row_count FROM slots UNION ALL SELECT 'reservations', count(*) FROM reservations UNION ALL SELECT 'jobs', count(*) FROM jobs;")
printf 'Restore test passed: %s\n' "$counts"

if [[ -n $recovered ]]; then
  install -m 0600 "$tmp/recovered.db" "$recovered"
  printf 'Wrote isolated recovered database: %s\n' "$recovered"
fi
