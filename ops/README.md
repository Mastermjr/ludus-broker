# Broker production operations

This directory contains public-safe automation for the dedicated broker
management VM. Deployment-specific state and SOPS ciphertext remain under the
ignored `.private/` directory.

## Sequence

1. `security/public-release-audit.sh` checks Git history and candidate files.
2. `proxmox/create-provisioner-token.sh` creates a short-lived scoped token.
3. `secrets/init-local-sops.sh` imports static secrets and removes plaintext.
4. `proxmox/provision-vm.sh` clones and starts the dedicated QEMU VM.
5. Bootstrap the operator SSH key and disable template password authentication.
6. `deploy/install.sh` installs the binary, units, service account, and unified
   `/var/lib/viberange-broker` tree.
7. `deploy/deploy-service-secrets.sh` installs root-owned systemd credential
   sources without writing plaintext locally.
8. Build and install the bounded native WireGuard plugin using
   `ludus-plugin/build-wireguard-plugin.sh` and `install-wireguard-plugin.sh`.
9. Validate plugin health, Discord resources, SQLite backup/restore, reboot
   recovery, and simulation mode before production lifecycle work.

The deployed environment reaches the private Ludus API directly, so the older
SSH tunnel unit is optional compatibility packaging rather than the production
path. Production startup now fails closed unless the authenticated WireGuard
plugin health probe succeeds. Keep the broker stopped until live profile,
revocation, stale-profile, and network-isolation acceptance passes.
