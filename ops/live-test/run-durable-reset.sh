#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
vm_state=${VM_STATE_FILE:-$repo_root/.private/vm.json}
slot=${1:-}
confirmation=${2:-}
[[ $slot =~ ^user[1-5]$ ]] || { echo "usage: run-durable-reset.sh <user1|...|user5> --yes" >&2; exit 2; }
[[ $confirmation == --yes ]] || { echo "error: destructive reset requires --yes" >&2; exit 2; }
[[ -s $vm_state ]] || { echo "error: missing ignored VM state: $vm_state" >&2; exit 1; }

jump=$(jq -r '.ssh_jump' "$vm_state")
target=$(jq -r '"\(.ssh_user)@\(.ip)"' "$vm_state")
ssh_command=(ssh -o LogLevel=ERROR -o BatchMode=yes -J "$jump" "$target")
stamp=$(date -u +%Y%m%dT%H%M%SZ)
evidence=${EVIDENCE_DIR:-$repo_root/.private/live-validation/$stamp-$slot-durable-reset}
umask 077
mkdir -p "$evidence"

"$repo_root/ops/live-test/preflight.sh" "$slot" >"$evidence/preflight-summary.json"
local_cli='sudo runuser -u viberange-broker -- env VBR_DATABASE_PATH=/var/lib/viberange-broker/state/broker.db VBR_SLOT_IDS=user1,user2,user3,user4,user5 /usr/local/bin/viberange-broker'
"${ssh_command[@]}" "$local_cli reset $slot --yes" >"$evidence/enqueue.txt"

started=$(date +%s)
set +e
"${ssh_command[@]}" "sudo bash -c 'set -a; . /var/lib/viberange-broker/broker.env; set +a; VBR_LUDUS_API_KEY_FILE=/var/lib/viberange-broker/credentials/ludus-api-key /usr/local/bin/viberange-broker run-job --yes'" \
  >"$evidence/worker-output.txt" 2>"$evidence/worker-error.txt"
worker_rc=$?
set -e
finished=$(date +%s)
printf '%s\n' "$((finished - started))" >"$evidence/duration-seconds"

"${ssh_command[@]}" "$local_cli slots --json" >"$evidence/slots-after.json"
"${ssh_command[@]}" "$local_cli jobs --json" >"$evidence/jobs-after.json"
if [[ $worker_rc -ne 0 ]]; then
  jq -n --arg slot "$slot" --argjson exit_code "$worker_rc" --arg evidence "$evidence" \
    '{slot:$slot,worker:"failed",exit_code:$exit_code,evidence_directory:$evidence}' | tee "$evidence/summary.json"
  exit "$worker_rc"
fi

jq -e --arg slot "$slot" '[.[] | select(.ID == $slot and .State == "AVAILABLE" and .LeaseOwner == "")] | length == 1' \
  "$evidence/slots-after.json" >/dev/null
jq -e --arg slot "$slot" '[.[] | select(.SlotID == $slot and .Transition == "RESET" and .State == "SUCCEEDED")] | length >= 1' \
  "$evidence/jobs-after.json" >/dev/null

(
  cd "$evidence"
  sha256sum ./* >SHA256SUMS
)
jq -n --arg slot "$slot" --arg evidence "$evidence" --argjson duration_seconds "$((finished - started))" \
  '{slot:$slot,durable_reset:"succeeded",duration_seconds:$duration_seconds,evidence_directory:$evidence}' \
  | tee "$evidence/summary.json"
