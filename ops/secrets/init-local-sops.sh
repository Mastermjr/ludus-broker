#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
private_dir="$repo_root/.private"
output=${SOPS_BUNDLE:-$private_dir/secrets.sops.yaml}
proxmox_json=${PROXMOX_TOKEN_FILE:-$private_dir/proxmox/proxmox-token.json}
discord_token=${DISCORD_TOKEN_FILE:-$HOME/.local/state/viberange/participant-broker/discord-bot-token}
ludus_key=${LUDUS_API_KEY_FILE:-$HOME/LUDUS_API_KEY}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
recipient=${SOPS_AGE_RECIPIENT:-age1razk8z8lplngy7swueuyu5gpwc6x2qtkymq0v7x38z4u623g5cksp45ete}

for command in jq sops ssh-keygen; do
  command -v "$command" >/dev/null || {
    echo "error: required command not found: $command" >&2
    exit 1
  }
done

for file in "$proxmox_json" "$discord_token" "$ludus_key" "$age_identity"; do
  [[ -s $file ]] || {
    echo "error: required secret input is missing or empty: $file" >&2
    exit 1
  }
done

if [[ -e $output && ${FORCE:-false} != true ]]; then
  echo "error: $output already exists; set FORCE=true only when intentionally rotating it" >&2
  exit 1
fi

umask 077
mkdir -p "$private_dir"
chmod 0700 "$private_dir"
tmp=$(mktemp -d /dev/shm/viberange-sops.XXXXXX)
trap 'rm -rf "$tmp"' EXIT

ssh-keygen -q -t ed25519 -N '' -C viberange-broker-operator -f "$tmp/operator-ssh" </dev/null

jq -n \
  --slurpfile proxmox "$proxmox_json" \
  --rawfile operator_private "$tmp/operator-ssh" \
  --rawfile operator_public "$tmp/operator-ssh.pub" \
  --rawfile discord_bot_token "$discord_token" \
  --rawfile ludus_admin_api_key "$ludus_key" \
  '{
    proxmox: $proxmox[0],
    operator: {
      ssh_private_key: ($operator_private | rtrimstr("\n")),
      ssh_public_key: ($operator_public | rtrimstr("\n"))
    },
    discord: {
      bot_token: ($discord_bot_token | rtrimstr("\n"))
    },
    ludus: {
      admin_api_key: ($ludus_admin_api_key | rtrimstr("\n"))
    }
  }' >"$tmp/plain.json"

SOPS_AGE_KEY_FILE="$age_identity" \
  sops --encrypt \
  --config /dev/null \
  --age "$recipient" \
  --input-type json \
  --output-type yaml \
  "$tmp/plain.json" >"$tmp/secrets.sops.yaml"

install -m 0600 "$tmp/secrets.sops.yaml" "$output"

# The Proxmox token is now recoverable from SOPS. Remove the plaintext local
# handoff while retaining the non-secret CA certificate.
rm -f "$proxmox_json"

SOPS_AGE_KEY_FILE="$age_identity" sops filestatus "$output"
echo "Encrypted local secret bundle created: $output"
echo "Back up the age identity separately; never commit either private path."
