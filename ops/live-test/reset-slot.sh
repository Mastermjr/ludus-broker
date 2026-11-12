#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
vm_state=${VM_STATE_FILE:-$repo_root/.private/vm.json}
slot=${1:-}
confirmation=${2:-}
[[ $slot =~ ^user[1-5]$ ]] || { echo "usage: reset-slot.sh <user1|...|user5> --yes" >&2; exit 2; }
[[ $confirmation == --yes ]] || { echo "error: destructive reset requires --yes" >&2; exit 2; }
[[ -s $vm_state ]] || { echo "error: missing ignored VM state: $vm_state" >&2; exit 1; }

for command in jq ssh; do
  command -v "$command" >/dev/null || { echo "error: required command missing: $command" >&2; exit 1; }
done

jump=$(jq -r '.ssh_jump' "$vm_state")
target=$(jq -r '"\(.ssh_user)@\(.ip)"' "$vm_state")
ssh_command=(ssh -o LogLevel=ERROR -o BatchMode=yes -J "$jump" "$target")
stamp=$(date -u +%Y%m%dT%H%M%SZ)
evidence=${EVIDENCE_DIR:-$repo_root/.private/live-validation/$stamp-$slot-reset}
umask 077
mkdir -p "$evidence"
chmod 0700 "$evidence"

# Run the complete non-mutating guard immediately before the mutation.
"$repo_root/ops/live-test/preflight.sh" "$slot" >"$evidence/preflight-summary.json"

remote_prefix='sudo bash -c '\''set -a; . /var/lib/viberange-broker/broker.env; set +a; VBR_LUDUS_API_KEY_FILE=/var/lib/viberange-broker/credentials/ludus-api-key'
started=$(date +%s)
set +e
"${ssh_command[@]}" "$remote_prefix /usr/local/bin/viberange-broker ludus reset $slot --yes --json'" \
  >"$evidence/reset-result.json" 2>"$evidence/reset-error.txt"
reset_rc=$?
set -e
finished=$(date +%s)
printf '%s\n' "$((finished - started))" >"$evidence/duration-seconds"

if [[ $reset_rc -ne 0 ]]; then
  jq -n --arg slot "$slot" --argjson exit_code "$reset_rc" --arg evidence "$evidence" \
    '{slot:$slot,reset:"failed",exit_code:$exit_code,evidence_directory:$evidence}' \
    | tee "$evidence/summary.json"
  exit "$reset_rc"
fi

jq -e --arg slot "$slot" '.user_id == $slot and .reset == "succeeded" and .readiness.ready == true' \
  "$evidence/reset-result.json" >/dev/null

# Capture all slots after the mutation to detect cross-slot effects.
: >"$evidence/all-slots-after.jsonl"
for user in user1 user2 user3 user4 user5; do
  "${ssh_command[@]}" "$remote_prefix /usr/local/bin/viberange-broker ludus inventory $user --json'" \
    | jq -c --arg user "$user" '{user:$user,id:.id,state:.state,testing:.testing,vm_count:(.vms|length),all_powered:([.vms[].powered_on]|all)}' \
    >>"$evidence/all-slots-after.jsonl"
done
jq -s -e 'length == 5 and all(.[]; .user == .id and .state == "SUCCESS" and .testing == true and .vm_count == 4 and .all_powered == true)' \
  "$evidence/all-slots-after.jsonl" >/dev/null

(
  cd "$evidence"
  sha256sum ./* >SHA256SUMS
)
jq -n \
  --arg slot "$slot" \
  --arg captured_at "$stamp" \
  --arg evidence "$evidence" \
  --argjson duration_seconds "$((finished - started))" \
  '{slot:$slot,captured_at:$captured_at,reset:"succeeded",duration_seconds:$duration_seconds,evidence_directory:$evidence}' \
  | tee "$evidence/summary.json"
