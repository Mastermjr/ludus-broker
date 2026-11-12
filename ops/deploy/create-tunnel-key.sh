#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "error: run as root on the broker VM" >&2
  exit 1
fi

ssh_host=${1:?usage: create-tunnel-key.sh <dfw-ssh-host>}
credential_dir=/var/lib/viberange-broker/credentials
key=$credential_dir/ludus-tunnel
known_hosts=$credential_dir/ludus-known-hosts

install -d -o root -g root -m 0700 "$credential_dir"
[[ ! -e $key && ! -e $key.pub ]] || {
  echo "error: refusing to overwrite existing tunnel identity: $key" >&2
  exit 1
}

ssh-keygen -q -t ed25519 -N '' -C viberange-broker-ludus-tunnel -f "$key" </dev/null
ssh-keyscan -H "$ssh_host" >"$known_hosts" 2>/dev/null
[[ -s $known_hosts ]] || { echo "error: could not collect SSH host key for $ssh_host" >&2; exit 1; }
chown root:root "$key" "$key.pub" "$known_hosts"
chmod 0600 "$key"
chmod 0644 "$key.pub" "$known_hosts"

printf '\nInstall this single restricted line in the target account authorized_keys:\n\n'
printf 'restrict,port-forwarding,permitopen="127.0.0.1:8080" %s\n' "$(cat "$key.pub")"
printf '\nDo not authorize the key without the restrictions above.\n'
