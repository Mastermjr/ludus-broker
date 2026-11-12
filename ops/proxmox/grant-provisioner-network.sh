#!/usr/bin/env bash
set -euo pipefail

# One-time compatibility fix for tokens created before the local-network ACL was
# included in create-provisioner-token.sh. Run as root on the Proxmox host.

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "error: run as root on the Proxmox host" >&2
  exit 1
fi

PVE_USER=${PVE_USER:-viberange-provisioner@pve}
TOKEN_NAME=${TOKEN_NAME:-bootstrap}
TOKEN_ID="${PVE_USER}!${TOKEN_NAME}"

pveum acl modify /sdn/zones/localnetwork \
  --users "$PVE_USER" \
  --roles PVESDNUser \
  --propagate 1
pveum acl modify /sdn/zones/localnetwork \
  --tokens "$TOKEN_ID" \
  --roles PVESDNUser \
  --propagate 1

echo "Granted SDN.Use on local bridges to the scoped provisioner user and token."
