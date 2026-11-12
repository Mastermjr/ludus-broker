#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bundle=${SOPS_BUNDLE:-$repo_root/.private/secrets.sops.yaml}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
vm_state=${VM_STATE_FILE:-$repo_root/.private/vm.json}

for command in sops jq ssh ssh-keygen timeout; do
  command -v "$command" >/dev/null || { echo "error: required command missing: $command" >&2; exit 1; }
done

[[ -s $vm_state ]] || { echo "error: missing ignored VM state: $vm_state" >&2; exit 1; }
jump_host=${JUMP_HOST:-$(jq -r '.ssh_jump' "$vm_state")}
broker_ip=$(jq -r '.ip' "$vm_state")
allowed_target=${ALLOWED_TARGET:-$broker_ip:22}
# Same host but a port not present in permitopen proves sshd rejects the
# destination before network reachability can influence the result.
denied_target=${DENIED_TARGET:-$broker_ip:2222}

umask 077
tmp=$(mktemp -d /dev/shm/viberange-jump-proof.XXXXXX)
cleanup() {
  if [[ -S $tmp/control ]]; then
    ssh -S "$tmp/control" -O exit "$jump_host" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

SOPS_AGE_KEY_FILE="$age_identity" sops --decrypt --output-type json "$bundle" >"$tmp/secrets.json"
jq -jr '.jump.ssh_private_key, "\n"' "$tmp/secrets.json" >"$tmp/jump"
chmod 0600 "$tmp/jump"
rm -f "$tmp/secrets.json"

ssh_options=(
  -i "$tmp/jump"
  -o IdentitiesOnly=yes
  -o BatchMode=yes
  -o ConnectTimeout=8
)

printf 'Jump key fingerprint: '
ssh-keygen -lf "$tmp/jump" | awk '{print $2, $4}'

set +e
ssh "${ssh_options[@]}" "$jump_host" 'printf shell-command-ran' >"$tmp/shell.out" 2>"$tmp/shell.err"
shell_rc=$?
set -e
if [[ $shell_rc -eq 0 || -s $tmp/shell.out ]]; then
  echo "FAIL: jump key executed a remote shell command" >&2
  exit 1
fi
echo "PASS: remote shell/command execution denied (exit $shell_rc, zero output)"

set +e
timeout 12 ssh "${ssh_options[@]}" -W "$denied_target" "$jump_host" \
  >"$tmp/denied.out" 2>"$tmp/denied.err"
denied_rc=$?
set -e
if [[ $denied_rc -eq 0 ]] || ! grep -qi 'administratively prohibited' "$tmp/denied.err"; then
  echo "FAIL: non-permitted forwarding was not explicitly denied" >&2
  sed -n '1,5p' "$tmp/denied.err" >&2
  exit 1
fi
echo "PASS: forwarding to $denied_target denied by sshd permitopen"

local_port=$((20000 + RANDOM % 20000))
ssh "${ssh_options[@]}" -M -S "$tmp/control" -fN \
  -o ExitOnForwardFailure=yes \
  -L "127.0.0.1:${local_port}:${allowed_target}" \
  "$jump_host"

banner=$(timeout 5 bash -c "exec 3<>/dev/tcp/127.0.0.1/$local_port; IFS= read -r line <&3; printf '%s' \"\$line\"")
if [[ $banner != SSH-2.0-* ]]; then
  echo "FAIL: permitted forwarding did not reach an SSH service" >&2
  exit 1
fi
echo "PASS: forwarding to $allowed_target reached ${banner%% *}"

echo "All restricted ProxyJump controls are enforced by the jump-host sshd."
