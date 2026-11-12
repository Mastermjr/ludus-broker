#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bundle=${SOPS_BUNDLE:-$repo_root/.private/secrets.sops.yaml}
age_identity=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}
vm_state=${VM_STATE_FILE:-$repo_root/.private/vm.json}
vm_name=${VM_NAME:-viberange-broker}
vmid=${VMID:-}
bridge=${BRIDGE:-vmbr1000}
cores=${CORES:-2}
memory=${MEMORY:-4096}

for command in sops jq curl; do
  command -v "$command" >/dev/null || {
    echo "error: required command not found: $command" >&2
    exit 1
  }
done
[[ -s $bundle ]] || { echo "error: missing SOPS bundle: $bundle" >&2; exit 1; }
[[ -s $age_identity ]] || { echo "error: missing age identity: $age_identity" >&2; exit 1; }
[[ ! -e $vm_state ]] || { echo "error: VM state already exists: $vm_state" >&2; exit 1; }

umask 077
tmp=$(mktemp -d /dev/shm/viberange-provision.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
SOPS_AGE_KEY_FILE="$age_identity" sops --decrypt --output-type json "$bundle" >"$tmp/secrets.json"

api=$(jq -r '.proxmox.api_url' "$tmp/secrets.json")
node=$(jq -r '.proxmox.node' "$tmp/secrets.json")
pool=$(jq -r '.proxmox.pool' "$tmp/secrets.json")
template_vmid=$(jq -r '.proxmox.template_vmid' "$tmp/secrets.json")
storage=$(jq -r '.proxmox.target_storage' "$tmp/secrets.json")
token_id=$(jq -r '.proxmox.token_id' "$tmp/secrets.json")
token_secret=$(jq -r '.proxmox.token_secret' "$tmp/secrets.json")

if [[ -n ${PVE_CA_BUNDLE:-} ]]; then
  ca_bundle=$PVE_CA_BUNDLE
elif [[ -n ${SSL_CERT_FILE:-} && -s ${SSL_CERT_FILE:-} ]]; then
  ca_bundle=$SSL_CERT_FILE
else
  echo "error: set PVE_CA_BUNDLE or SSL_CERT_FILE to a trusted CA bundle" >&2
  exit 1
fi

cat >"$tmp/curl.conf" <<EOF
silent
show-error
fail-with-body
noproxy = "*"
cacert = "$ca_bundle"
header = "Authorization: PVEAPIToken=${token_id}=${token_secret}"
EOF
unset token_secret

api_request() {
  local method=$1 path=$2
  shift 2
  : >"$tmp/response.json"
  if ! curl --config "$tmp/curl.conf" --request "$method" "$@" \
      "$api/api2/json$path" >"$tmp/response.json"; then
    echo "error: Proxmox API request failed: $method $path" >&2
    jq -r '.errors // .data // empty' "$tmp/response.json" >&2 2>/dev/null || true
    return 1
  fi
}

wait_task() {
  local upid=$1 encoded status exitstatus
  encoded=$(jq -rn --arg value "$upid" '$value|@uri')
  for _ in $(seq 1 240); do
    api_request GET "/nodes/$node/tasks/$encoded/status"
    status=$(jq -r '.data.status' "$tmp/response.json")
    if [[ $status == stopped ]]; then
      exitstatus=$(jq -r '.data.exitstatus // "unknown"' "$tmp/response.json")
      [[ $exitstatus == OK ]] || {
        echo "error: Proxmox task failed: $exitstatus" >&2
        return 1
      }
      return 0
    fi
    sleep 3
  done
  echo "error: timed out waiting for Proxmox task" >&2
  return 1
}

if [[ -z $vmid ]]; then
  api_request GET /cluster/nextid
  vmid=$(jq -r '.data' "$tmp/response.json")
fi

if api_request GET "/nodes/$node/qemu/$vmid/status/current" 2>/dev/null; then
  echo "error: target VMID already exists: $vmid" >&2
  exit 1
fi

printf 'Cloning template %s to VM %s (%s)...\n' "$template_vmid" "$vmid" "$vm_name"
api_request POST "/nodes/$node/qemu/$template_vmid/clone" \
  --data-urlencode "newid=$vmid" \
  --data-urlencode "name=$vm_name" \
  --data-urlencode "full=1" \
  --data-urlencode "storage=$storage" \
  --data-urlencode "pool=$pool" \
  --data-urlencode "description=Dedicated VibeRange broker management VM; not managed by Ludus"
wait_task "$(jq -r '.data' "$tmp/response.json")"

api_request GET "/nodes/$node/qemu/$vmid/config"
net0=$(jq -r '.data.net0' "$tmp/response.json")
mac=$(sed -n 's/^virtio=\([^,]*\).*/\1/p' <<<"$net0")
[[ -n $mac ]] || { echo "error: cloned VM has no parseable virtio MAC" >&2; exit 1; }

api_request PUT "/nodes/$node/qemu/$vmid/config" \
  --data-urlencode "cores=$cores" \
  --data-urlencode "memory=$memory" \
  --data-urlencode "onboot=1" \
  --data-urlencode "agent=enabled=1" \
  --data-urlencode "net0=virtio=$mac,bridge=$bridge,firewall=1" \
  --data-urlencode "tags=viberange-broker;management"

api_request POST "/nodes/$node/qemu/$vmid/status/start"
wait_task "$(jq -r '.data' "$tmp/response.json")"

mkdir -p "$(dirname "$vm_state")"
jq -n \
  --arg api_url "$api" \
  --arg node "$node" \
  --arg pool "$pool" \
  --arg name "$vm_name" \
  --arg bridge "$bridge" \
  --arg mac "$mac" \
  --argjson vmid "$vmid" \
  --argjson template_vmid "$template_vmid" \
  '{api_url:$api_url,node:$node,pool:$pool,vmid:$vmid,name:$name,template_vmid:$template_vmid,bridge:$bridge,mac:$mac}' \
  >"$vm_state"
chmod 0600 "$vm_state"

printf 'VM %s (%s) is running. State written to %s\n' "$vmid" "$vm_name" "$vm_state"
printf 'Next: discover its DHCP address, install the operator key, and disable password authentication.\n'
