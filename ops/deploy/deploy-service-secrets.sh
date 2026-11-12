#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bundle=${SOPS_BUNDLE:-$repo_root/.private/secrets.sops.yaml}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
target=${1:?usage: deploy-service-secrets.sh <operator@broker-host>}
ssh_command=(ssh -o BatchMode=yes)
if [[ -n ${VBR_SSH_JUMP:-} ]]; then
  ssh_command+=(-J "$VBR_SSH_JUMP")
fi

for command in sops jq ssh; do
  command -v "$command" >/dev/null || { echo "error: required command not found: $command" >&2; exit 1; }
done

umask 077
tmp=$(mktemp /dev/shm/viberange-service-secrets.XXXXXX)
trap 'rm -f "$tmp"' EXIT
SOPS_AGE_KEY_FILE="$age_identity" sops --decrypt --output-type json "$bundle" >"$tmp"

"${ssh_command[@]}" "$target" 'sudo install -d -o root -g root -m 0700 /var/lib/viberange-broker/credentials'

install_secret() {
  local jq_path=$1 remote_name=$2
  # remote_name is selected only from fixed call sites below.
  # shellcheck disable=SC2029
  jq -er "$jq_path" "$tmp" |
    "${ssh_command[@]}" "$target" "sudo sh -c 'umask 077; cat > /var/lib/viberange-broker/credentials/$remote_name; chown root:root /var/lib/viberange-broker/credentials/$remote_name; chmod 0600 /var/lib/viberange-broker/credentials/$remote_name'"
}

install_secret '.discord.bot_token' discord-token
install_secret '.ludus.admin_api_key' ludus-api-key

echo "Installed root-owned Discord and Ludus service credential sources on $target."
