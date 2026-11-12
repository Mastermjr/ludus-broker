#!/usr/bin/env bash
set -euo pipefail

source_db=${VBR_DATABASE_PATH:-/var/lib/viberange-broker/state/broker.db}
output_dir=${1:?usage: backup-broker.sh <encrypted-backup-directory>}
recipient=${VBR_BACKUP_AGE_RECIPIENT:-age1razk8z8lplngy7swueuyu5gpwc6x2qtkymq0v7x38z4u623g5cksp45ete}

for command in sqlite3 age sha256sum; do
  command -v "$command" >/dev/null || { echo "error: required command not found: $command" >&2; exit 1; }
done
[[ -s $source_db ]] || { echo "error: broker database is missing or empty: $source_db" >&2; exit 1; }

umask 077
install -d -m 0700 "$output_dir"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
stamp=$(date -u +%Y%m%dT%H%M%SZ)
base="viberange-broker-${stamp}"
snapshot="$tmp/$base.db"
encrypted="$output_dir/$base.db.age"
manifest="$output_dir/$base.manifest"

# SQLite's online backup command includes committed WAL contents in a consistent
# snapshot without requiring the live service to stop.
sqlite3 "$source_db" ".timeout 30000" ".backup '$snapshot'"
[[ $(sqlite3 "$snapshot" 'PRAGMA integrity_check;') == ok ]] || {
  echo "error: SQLite integrity check failed before encryption" >&2
  exit 1
}

age --encrypt --recipient "$recipient" --output "$tmp/$base.db.age" "$snapshot"
install -m 0600 "$tmp/$base.db.age" "$encrypted"
{
  printf 'created_utc=%s\n' "$stamp"
  printf 'format=sqlite-age-v1\n'
  printf 'encrypted_file=%s\n' "$(basename "$encrypted")"
  printf 'sha256='
  sha256sum "$encrypted" | awk '{print $1}'
} >"$manifest"
chmod 0600 "$manifest"

printf 'Created encrypted SQLite backup: %s\n' "$encrypted"
printf 'Manifest: %s\n' "$manifest"
