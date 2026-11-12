# Proxmox broker VM bootstrap

The broker management VM is a dedicated QEMU guest outside every Ludus-managed
participant range. Proxmox administrator access is needed only to create a
short-lived, privilege-separated API token. Revoke the token after provisioning.

## 1. Select the source template and storage

As root on the Proxmox host:

```bash
qm list
pvesm status
```

Select a Debian 12 cloud-init template and the destination storage. Do not select
a participant VM or place the broker VM in a Ludus pool.

## 2. Create the short-lived token

Copy `create-provisioner-token.sh` to the Proxmox host, review it, and run:

```bash
chmod 0700 create-provisioner-token.sh
sudo env \
  TEMPLATE_VMID=<debian-12-template-vmid> \
  TARGET_STORAGE=local-lvm \
  API_URL=https://proxmox.example:8006 \
  ./create-provisioner-token.sh
```

Optional environment variables include `POOL`, `PVE_USER`, `TOKEN_NAME`,
`NODE`, `EXPIRE_EPOCH`, and `OUTPUT_DIR`. The default token expires after seven
days.

The script creates a dedicated empty pool, API-only Proxmox user, custom scoped
roles, local-bridge `SDN.Use`, and a privilege-separated token. It writes the
token and cluster CA to a root-only handoff directory instead of printing the
secret.

Tokens created by an older copy of the script may be missing permission to use
the template's local bridge. In that case, run
`grant-provisioner-network.sh` once as root before retrying the clone.

Securely import `proxmox-token.json` into the ignored local SOPS bundle. Never
paste it into chat or commit it. The CA certificate is public but deployment
specific and should remain in the ignored private workspace.

## 3. Revoke after provisioning

After the broker VM exists and operator SSH works:

```bash
pveum user token remove viberange-provisioner@pve bootstrap
```

The VM continues running after token revocation. Further configuration occurs
over operator SSH and does not require permanent Proxmox credentials.
