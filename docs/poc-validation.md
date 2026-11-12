# Proof-of-concept validation

## Current scope

The standalone broker, dedicated management VM, Discord command boundary, range
reset worker, durable SQLite jobs, backup/restore, and five-slot out-of-band
reset validation are implemented. The production service is intentionally
stopped while generation-bound WireGuard delivery is installed and accepted.

## Validated foundations

- one Go binary with `serve` and local operator subcommands;
- SQLite WAL queue, slots, cooldowns, WireGuard metadata, and durable jobs;
- `/range join`, `/range leave`, and `/range status` scoped to one guild/channel;
- every interaction response is ephemeral;
- participant-role validation immediately before assignment;
- dedicated hardened broker VM, systemd packaging, NTP, firewall, and encrypted backups;
- pinned-TLS Ludus REST client and canonical blueprint guard;
- typed reset/rebaseline sequence and four-VM readiness validation;
- sequential durable reset/rebaseline validation for `user1` through `user5`;
- interrupted-job recovery and idempotent replay validation;
- production WireGuard adapter contract, private DM attachment path, generation metadata, expiry/release revocation, and handshake-based no-show reclamation;
- native Ludus WireGuard plugin source, compatible build script, atomic peer replacement, generation-bound revocation, and unit tests.

## Automated tests

The standard gate is:

```text
go test ./...
go vet ./...
git diff --check
shellcheck deployment and operations scripts
public-release secret scan
compatible Ludus plugin build
```

Coverage includes:

- queue uniqueness, FIFO ordering, cancellation, cooldowns, and active leases;
- PREPARING claim/commit/rollback and generation checks;
- durable job retries, quarantine, restart recovery, and idempotency;
- exact guild/channel/role checks and private interaction responses;
- private WireGuard DM handoff and DM-failure revoke/reset rollback;
- rejection of wrong-user or wrong-generation plugin responses;
- non-persistence and non-serialization of WireGuard profiles;
- atomic plugin configuration replacement and rollback on `wg syncconf` failure;
- generation-bound idempotent peer revocation;
- lease expiry and no-show reset creation from WireGuard handshake status;
- Ludus certificate pinning, canonical blueprint checks, reset order, and readiness.

## Existing live evidence

Read-only inventory for all five slots returned matching range identity,
`SUCCESS`, testing enabled, four expected VMs, and all VMs powered on. The
canonical release blueprint was established. The live configurations now carry
approved slot-specific `always_blocked_networks` overlays; the broker validates
a complete exact per-slot hash map and rejects any unapproved drift.

Discord-free durable reset/rebaseline jobs succeeded sequentially for all five
slots. The ABI-matched Ludus 2.3 plugin was reconstructed from the installed
server's exact build graph and embedded static inputs, loaded successfully, and
reported healthy. Every slot passed live generation-bound issue/status/revoke;
stale-generation rejection and post-issue/pre-commit recovery revocation also
passed. No participant API-key delivery path remains.

The dedicated broker VM's SQLite state survived reboot. Encrypted online backup,
off-VM copy, restore, and integrity testing passed. Required Discord and Ludus
egress and restricted management ingress were verified.

Human user1 acceptance passed `/range join`, `/range status`, and `/range leave`,
three-file private DM delivery, real WireGuard handshake, generation revocation,
30-minute cooldown and audited thaw, assigned CH-UI/Jupyter/Caldera access, and
adversarial same-range, cross-range, Ludus, Proxmox, and management denial. The
plugin v1.1 router cleanup passed direct and integrated reset validation, and
post-reset CH-UI stale-PID recovery was independently confirmed.

## Remaining production gates

The supervised-testing release has two delegated evidence tasks:

- repeat final WireGuard acceptance on user2–user5;
- publish the sanitized all-slot acceptance record and final operations notes.

Unattended reboot persistence, enable-at-boot, and formal production cutover are
explicitly deferred. The current service may be run for supervised testing but
must not be represented as completed unattended production cutover.

No normal-user browser automation or synthetic Discord user account is used.
