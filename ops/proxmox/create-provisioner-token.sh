#!/usr/bin/env bash
set -euo pipefail

# Run as root on the Proxmox VE host. This creates a short-lived,
# privilege-separated API token that can clone one or more VMs into a dedicated
# pool without granting access to Ludus participant pools.
#
# Required environment:
#   TEMPLATE_VMID=9000       Debian 12 cloud-init template VMID
#   TARGET_STORAGE=local-lvm destination storage ID
#
# Optional environment:
#   POOL=VIBERANGE-BROKER
#   PVE_USER=viberange-provisioner@pve
#   TOKEN_NAME=bootstrap
#   NODE=$(hostname -s)
#   API_URL=https://proxmox.example:8006
#   EXPIRE_EPOCH=<unix time>  defaults to seven days from creation
#   OUTPUT_DIR=/root/viberange-provisioner-handoff

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "error: run this script as root on the Proxmox host" >&2
  exit 1
fi

for command in pveum pvesh jq; do
  command -v "$command" >/dev/null || {
    echo "error: required command not found: $command" >&2
    exit 1
  }
done

: "${TEMPLATE_VMID:?set TEMPLATE_VMID to the Debian 12 cloud-init template VMID}"
: "${TARGET_STORAGE:?set TARGET_STORAGE to the destination Proxmox storage ID}"

POOL=${POOL:-VIBERANGE-BROKER}
PVE_USER=${PVE_USER:-viberange-provisioner@pve}
TOKEN_NAME=${TOKEN_NAME:-bootstrap}
NODE=${NODE:-$(hostname -s)}
API_URL=${API_URL:-https://$(hostname -f):8006}
EXPIRE_EPOCH=${EXPIRE_EPOCH:-$(date -d '+7 days' +%s)}
OUTPUT_DIR=${OUTPUT_DIR:-/root/viberange-provisioner-handoff}
TOKEN_ID="${PVE_USER}!${TOKEN_NAME}"

POOL_ROLE=VBRBrokerProvisioner
TEMPLATE_ROLE=VBRBrokerTemplateClone
STORAGE_ROLE=VBRBrokerStorage
AUDIT_ROLE=VBRBrokerNodeAudit

POOL_PRIVS="Pool.Allocate Pool.Audit VM.Allocate VM.Audit VM.Clone VM.Config.CDROM VM.Config.Cloudinit VM.Config.CPU VM.Config.Disk VM.Config.HWType VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt"
TEMPLATE_PRIVS="VM.Audit VM.Clone"
STORAGE_PRIVS="Datastore.AllocateSpace Datastore.Audit"
AUDIT_PRIVS="Sys.Audit"

umask 077
mkdir -p "$OUTPUT_DIR"
chmod 0700 "$OUTPUT_DIR"

pvesh get "/nodes/${NODE}/qemu/${TEMPLATE_VMID}/status/current" >/dev/null
pvesh get "/nodes/${NODE}/storage/${TARGET_STORAGE}/status" >/dev/null

if ! pveum pool list --output-format json | jq -e --arg pool "$POOL" '.[] | select(.poolid == $pool)' >/dev/null; then
  pveum pool add "$POOL" --comment "Dedicated VibeRange broker management VM"
fi

upsert_role() {
  local role=$1 privileges=$2
  if pveum role list --output-format json | jq -e --arg role "$role" '.[] | select(.roleid == $role)' >/dev/null; then
    pveum role modify "$role" --privs "$privileges"
  else
    pveum role add "$role" --privs "$privileges"
  fi
}

upsert_role "$POOL_ROLE" "$POOL_PRIVS"
upsert_role "$TEMPLATE_ROLE" "$TEMPLATE_PRIVS"
upsert_role "$STORAGE_ROLE" "$STORAGE_PRIVS"
upsert_role "$AUDIT_ROLE" "$AUDIT_PRIVS"

if ! pveum user list --output-format json | jq -e --arg user "$PVE_USER" '.[] | select(.userid == $user)' >/dev/null; then
  pveum user add "$PVE_USER" --comment "Temporary VibeRange broker VM provisioner"
fi

apply_acls() {
  local principal_flag=$1 principal=$2
  pveum acl modify "/pool/${POOL}" "$principal_flag" "$principal" --roles "$POOL_ROLE" --propagate 1
  pveum acl modify "/vms/${TEMPLATE_VMID}" "$principal_flag" "$principal" --roles "$TEMPLATE_ROLE" --propagate 0
  pveum acl modify "/storage/${TARGET_STORAGE}" "$principal_flag" "$principal" --roles "$STORAGE_ROLE" --propagate 0
  pveum acl modify "/nodes/${NODE}" "$principal_flag" "$principal" --roles "$AUDIT_ROLE" --propagate 0
  # Proxmox checks SDN.Use even for local Linux bridges selected by a VM.
  pveum acl modify /sdn/zones/localnetwork "$principal_flag" "$principal" --roles PVESDNUser --propagate 1
}

# A privilege-separated token receives the intersection of its own ACLs and its
# backing user's ACLs, so both principals must receive the same scoped grants.
apply_acls --users "$PVE_USER"

if pveum user token list "$PVE_USER" --output-format json | jq -e --arg token "$TOKEN_NAME" '.[] | select(.tokenid == $token)' >/dev/null; then
  echo "error: token ${TOKEN_ID} already exists; refusing to rotate it implicitly" >&2
  echo "delete it explicitly with: pveum user token remove '$PVE_USER' '$TOKEN_NAME'" >&2
  exit 1
fi

token_json=$(pveum user token add "$PVE_USER" "$TOKEN_NAME" \
  --privsep 1 \
  --expire "$EXPIRE_EPOCH" \
  --comment "Short-lived VibeRange broker VM provisioning token" \
  --output-format json)

apply_acls --tokens "$TOKEN_ID"

jq -n \
  --arg api_url "$API_URL" \
  --arg node "$NODE" \
  --arg pool "$POOL" \
  --arg template_vmid "$TEMPLATE_VMID" \
  --arg target_storage "$TARGET_STORAGE" \
  --arg token_id "$(jq -r '."full-tokenid" // empty' <<<"$token_json")" \
  --arg token_secret "$(jq -r '.value // empty' <<<"$token_json")" \
  --argjson expires "$EXPIRE_EPOCH" \
  '{
    api_url: $api_url,
    node: $node,
    pool: $pool,
    template_vmid: ($template_vmid | tonumber),
    target_storage: $target_storage,
    token_id: $token_id,
    token_secret: $token_secret,
    expires: $expires
  }' >"$OUTPUT_DIR/proxmox-token.json"

if [[ ! -s "$OUTPUT_DIR/proxmox-token.json" ]] ||
   [[ $(jq -r '.token_id == "" or .token_secret == ""' "$OUTPUT_DIR/proxmox-token.json") == true ]]; then
  echo "error: Proxmox did not return a usable token; revoke ${TOKEN_ID} and retry" >&2
  exit 1
fi

cp /etc/pve/pve-root-ca.pem "$OUTPUT_DIR/pve-root-ca.pem"
chmod 0600 "$OUTPUT_DIR/proxmox-token.json"
chmod 0644 "$OUTPUT_DIR/pve-root-ca.pem"

cat <<EOF
Created privilege-separated token: $TOKEN_ID
Dedicated pool:                    $POOL
Template VMID:                     $TEMPLATE_VMID
Destination storage:              $TARGET_STORAGE
Expires:                           $(date -d "@$EXPIRE_EPOCH" --iso-8601=seconds)

Root-only handoff bundle:
  $OUTPUT_DIR/proxmox-token.json
  $OUTPUT_DIR/pve-root-ca.pem

Do not paste the token into chat or commit it. Import proxmox-token.json into
the ignored local SOPS bundle, then securely delete the handoff copy. Revoke the
token after the management VM has been provisioned:
  pveum user token remove '$PVE_USER' '$TOKEN_NAME'
EOF
