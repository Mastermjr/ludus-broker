#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
vm_state=${VM_STATE_FILE:-$repo_root/.private/vm.json}
slot=${1:?usage: preflight.sh <user1|user2|user3|user4|user5>}
[[ $slot =~ ^user[1-5]$ ]] || { echo "error: invalid participant slot: $slot" >&2; exit 2; }
[[ -s $vm_state ]] || { echo "error: missing ignored VM state: $vm_state" >&2; exit 1; }

for command in jq ssh sha256sum; do
  command -v "$command" >/dev/null || { echo "error: required command missing: $command" >&2; exit 1; }
done

jump=$(jq -r '.ssh_jump' "$vm_state")
target=$(jq -r '"\(.ssh_user)@\(.ip)"' "$vm_state")
ssh_command=(ssh -o LogLevel=ERROR -o BatchMode=yes -J "$jump" "$target")
stamp=$(date -u +%Y%m%dT%H%M%SZ)
evidence=${EVIDENCE_DIR:-$repo_root/.private/live-validation/$stamp-$slot-preflight}
umask 077
mkdir -p "$evidence"
chmod 0700 "$evidence"

service_state=$("${ssh_command[@]}" 'systemctl is-active viberange-broker.service || true')
[[ $service_state == inactive ]] || {
  echo "error: Discord broker service must be inactive, got: $service_state" >&2
  exit 1
}
printf '%s\n' "$service_state" >"$evidence/broker-service-state.txt"

ntp=$("${ssh_command[@]}" 'timedatectl show -p NTPSynchronized --value')
[[ $ntp == yes ]] || { echo "error: management VM clock is not synchronized" >&2; exit 1; }
printf '%s\n' "$ntp" >"$evidence/ntp-synchronized.txt"

remote_local_cli='sudo runuser -u viberange-broker -- env VBR_DATABASE_PATH=/var/lib/viberange-broker/state/broker.db VBR_SLOT_IDS=user1,user2,user3,user4,user5 /usr/local/bin/viberange-broker'
"${ssh_command[@]}" "$remote_local_cli slots --json" >"$evidence/slots.json"
"${ssh_command[@]}" "$remote_local_cli queue --json" >"$evidence/queue.json"
"${ssh_command[@]}" "$remote_local_cli jobs --json" >"$evidence/jobs.json"

jq -e --arg slot "$slot" '
  length == 5 and
  ([.[] | select(.ID == $slot and .State == "AVAILABLE" and .LeaseOwner == "")] | length == 1)
' "$evidence/slots.json" >/dev/null || {
  echo "error: selected slot is not uniquely AVAILABLE without a lease" >&2
  exit 1
}
jq -e '(. // []) | length == 0' "$evidence/queue.json" >/dev/null || {
  echo "error: active queue or lease state exists" >&2
  exit 1
}
jq -e '[(. // [])[] | select(.State == "PENDING" or .State == "RUNNING")] | length == 0' "$evidence/jobs.json" >/dev/null || {
  echo "error: pending or running lifecycle job exists" >&2
  exit 1
}

"${ssh_command[@]}" "sudo bash -c 'set -a; . /var/lib/viberange-broker/broker.env; set +a; VBR_LUDUS_API_KEY_FILE=/var/lib/viberange-broker/credentials/ludus-api-key /usr/local/bin/viberange-broker ludus preflight $slot --json'" >"$evidence/ludus-preflight.json"
jq -e --arg slot "$slot" '
  .user_id == $slot and .inventory.id == $slot and .inventory.state == "SUCCESS" and
  .inventory.testing == true and (.inventory.vms|length) == 4 and
  ([.inventory.vms[].powered_on] | all) and .readiness.ready == true
' "$evidence/ludus-preflight.json" >/dev/null || {
  echo "error: authoritative Ludus preflight invariants failed" >&2
  exit 1
}

# Require a fresh, successful encrypted DB backup immediately before mutation.
"${ssh_command[@]}" 'sudo systemctl start viberange-broker-backup.service'
"${ssh_command[@]}" 'sudo systemctl is-failed --quiet viberange-broker-backup.service && exit 1 || true; sudo find /var/backups/viberange-broker -maxdepth 1 -type f -printf "%TY-%Tm-%TdT%TH:%TM:%TSZ %f %s\n" | sort' >"$evidence/backups.txt"
[[ -s $evidence/backups.txt ]] || { echo "error: no encrypted backup evidence found" >&2; exit 1; }

(
  cd "$evidence"
  sha256sum ./* >SHA256SUMS
)

jq -n \
  --arg slot "$slot" \
  --arg captured_at "$stamp" \
  --arg evidence "$evidence" \
  --arg blueprint_sha256 "$(jq -r '.blueprint_sha256' "$evidence/ludus-preflight.json")" \
  '{slot:$slot,captured_at:$captured_at,evidence_directory:$evidence,blueprint_sha256:$blueprint_sha256,preflight:"passed"}' \
  | tee "$evidence/summary.json"
