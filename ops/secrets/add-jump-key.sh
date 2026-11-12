#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bundle=${SOPS_BUNDLE:-$repo_root/.private/secrets.sops.yaml}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
recipient=${SOPS_AGE_RECIPIENT:-age1razk8z8lplngy7swueuyu5gpwc6x2qtkymq0v7x38z4u623g5cksp45ete}

for command in sops jq ssh-keygen; do
  command -v "$command" >/dev/null || { echo "error: required command not found: $command" >&2; exit 1; }
done
[[ -s $bundle ]] || { echo "error: missing SOPS bundle: $bundle" >&2; exit 1; }

umask 077
tmp=$(mktemp -d /dev/shm/viberange-jump-key.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
SOPS_AGE_KEY_FILE="$age_identity" sops --decrypt --output-type json "$bundle" >"$tmp/plain.json"
if jq -e '.jump.ssh_private_key | type == "string" and length > 0' "$tmp/plain.json" >/dev/null; then
  echo "error: jump key already exists in SOPS bundle" >&2
  exit 1
fi

ssh-keygen -q -t ed25519 -N '' -C viberange-broker-proxyjump -f "$tmp/jump" </dev/null
jq --rawfile private "$tmp/jump" --rawfile public "$tmp/jump.pub" \
  '.jump = {ssh_private_key: ($private|rtrimstr("\n")), ssh_public_key: ($public|rtrimstr("\n"))}' \
  "$tmp/plain.json" >"$tmp/updated.json"

SOPS_AGE_KEY_FILE="$age_identity" sops --encrypt --config /dev/null --age "$recipient" \
  --input-type json --output-type yaml "$tmp/updated.json" >"$tmp/updated.sops.yaml"
install -m 0600 "$tmp/updated.sops.yaml" "$bundle"
SOPS_AGE_KEY_FILE="$age_identity" sops filestatus "$bundle"
echo "Added dedicated ProxyJump keypair to encrypted SOPS bundle."
