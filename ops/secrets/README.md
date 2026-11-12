# Local secret workflow

Deployment secrets are encrypted in the ignored file
`.private/secrets.sops.yaml`. The age identity is outside the repository at
`~/.config/sops/age/keys.txt` and must be backed up separately in an approved
password manager or offline recovery store.

The encrypted bundle contains the short-lived Proxmox provisioning token, the
operator SSH keypair, Discord bot token, and Ludus administrator API key. The
restricted Ludus tunnel key is generated on the broker VM and is not part of
the local bundle.

Initialize once after securely staging `proxmox-token.json`:

```bash
nix shell nixpkgs#sops -c ops/secrets/init-local-sops.sh
```

The initializer generates the operator key in tmpfs, encrypts all static
secrets, and removes the plaintext local Proxmox token. It refuses to overwrite
an existing bundle unless `FORCE=true` is explicitly supplied.

Decrypt only for a bounded operation, preferably into tmpfs or directly into an
SSH agent. Never print decrypted values, pass tokens as command-line arguments,
or commit `.private/`. Run the public-release audit before publishing:

```bash
nix shell nixpkgs#gitleaks -c ops/security/public-release-audit.sh
```
