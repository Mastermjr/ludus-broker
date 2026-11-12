# Ludus integration strategy

## Supported baseline

The broker targets the Ludus 2.x REST API and the native Ludus Go plugin loader.
REST authentication uses the protected broker administrator API key in the
`X-API-KEY` header. Participants never receive this key.

## Compatibility boundary

`internal/ludus.Client` defines the capabilities required by the broker:

- report server/API version;
- resolve authoritative range and VM inventory;
- validate the canonical blueprint;
- reset one range to its clean testing baseline;
- verify post-reset readiness;
- issue an exact generation's WireGuard peer and transient profile;
- revoke an exact generation's peer;
- return non-secret peer and latest-handshake status;
- clear persistent router capture/runtime state before workload rollback.

Queueing, Discord, persistence, scheduling, state transitions, and audit logic
do not know plugin implementation details.

## WireGuard plugin

Stock Ludus creates a user's WireGuard keypair once and returns that same saved
profile. The VibeRange plugin is loaded by the root-context `ludus-admin`
process and registers bounded authenticated routes beneath
`/api/v2/user/viberange-wireguard`.

The user-facing Ludus API proxies these `/user` operations to the admin service,
so the broker continues to use its existing pinned HTTPS connection and API
credential. No broker SSH access, new port, or Ludus binary modification is
required.

The plugin:

- accepts only `user1` through `user5`;
- requires an administrator credential;
- validates positive issue generations and generation-exact revocation;
- generates a fresh client keypair;
- atomically replaces the user's marked peer block in `wg0.conf`;
- applies it without interface restart using `wg syncconf`;
- returns the profile only in the issue response;
- stores only public peer/revocation metadata;
- removes Ludus' stock per-user private-key file after issue;
- reports the latest handshake for no-show reclamation;
- invokes a bounded Proxmox guest-agent cleanup only for the matching
  `user1`–`user5` router, stopping capture units, clearing router run/runtime,
  Zeek, and Vector transient state, restarting Vector, and requiring a zero
  guest exit status.

The plugin must be compiled with the exact compatible Ludus source and Go toolchain:

```bash
ops/ludus-plugin/build-wireguard-plugin.sh /path/to/ludus-api ./viberange-wireguard.so
sudo ops/ludus-plugin/install-wireguard-plugin.sh ./viberange-wireguard.so /opt/ludus
```

## Required operational behavior

The adapter and worker must:

- target stable slot IDs and exact generations;
- reject incomplete or wrong-user plugin responses;
- revoke the peer before range cleanup;
- preserve failure detail before retrying;
- quarantine after bounded failures;
- never log or persist the WireGuard profile/private key;
- treat a stale-generation revoke as failure rather than revoking a newer peer;
- reclaim assignments with no post-issue handshake after the configured grace period.

## Reset sequence

1. revoke the exact current WireGuard generation;
2. invoke the plugin's bounded router cleanup and require its guest-agent
   command to succeed; this stops capture units, discards router-local run and
   runtime artifacts, clears transient Zeek/Vector spool and checkpoint state,
   and restarts Vector;
3. stop Ludus testing with `force=false` to restore the workload baseline;
4. power on all expected VMs;
5. verify range state, inventory, service readiness, and firewall identity;
6. restart testing to establish the clean next workload baseline;
7. verify readiness again;
8. return the slot to `AVAILABLE` only after durable job completion.

Ludus testing snapshots do not include the separately managed router. A router
capture can therefore survive a successful workload snapshot revert. Merely
reporting an old capture as `stopped` is insufficient: the presence of
`active.env` makes the next experiment fail with `a capture is already active`.
Any ambiguous stop/seal/cleanup result quarantines the slot rather than making
it available.
