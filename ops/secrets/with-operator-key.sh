#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bundle=${SOPS_BUNDLE:-$repo_root/.private/secrets.sops.yaml}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}

[[ $# -gt 0 ]] || { echo "usage: $0 <command> [args...]" >&2; exit 2; }
for command in sops jq ssh-agent ssh-add; do
  command -v "$command" >/dev/null || { echo "error: required command not found: $command" >&2; exit 1; }
done

umask 077
tmp=$(mktemp /dev/shm/viberange-operator.XXXXXX)
agent_started=false
cleanup() {
  rm -f "$tmp"
  if [[ $agent_started == true ]]; then
    ssh-agent -k >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

SOPS_AGE_KEY_FILE="$age_identity" sops --decrypt --output-type json "$bundle" >"$tmp"
eval "$(ssh-agent -s)" >/dev/null
agent_started=true
jq -r '.operator.ssh_private_key' "$tmp" | ssh-add - >/dev/null
if jq -e '.jump.ssh_private_key | type == "string" and length > 0' "$tmp" >/dev/null 2>&1; then
  jq -r '.jump.ssh_private_key' "$tmp" | ssh-add - >/dev/null
fi
rm -f "$tmp"

"$@"
