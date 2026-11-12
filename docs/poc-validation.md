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

Live Discord acceptance established three simultaneous healthy leases on
user1–user3 for distinct participants, each with a recorded WireGuard handshake.
The queue preserved FIFO claims and delivered private ZIP/Markdown handoffs;
`/range status` and `/range leave` exercised assigned and draining states.
User1 additionally passed generation revocation, 30-minute cooldown and audited
thaw, assigned CH-UI/Jupyter/Caldera access, and adversarial same-range,
cross-range, Ludus, Proxmox, and management denial.

Discord-free user4 and user5 acceptance each issued a fresh protected profile,
established a real handshake, reached assigned CH-UI, JupyterLab, and Caldera,
passed an operator-side denied-service probe, always tore the interface down,
revoked the exact generation, deleted transient profile material, and reconciled
the available broker generation. Together these runs cover all five slots. The
plugin v1.1 router cleanup passed direct and integrated reset validation, and
post-reset CH-UI stale-PID recovery was independently confirmed.

## Completed production gates

The broker and encrypted-backup timer are enabled across reboot. Two consecutive
VM reboots confirmed SQLite state preservation and automatic recovery: the
first service attempt may fail closed before the private route is installed,
then systemd retry connects to plugin v1.1.0, validates every approved slot,
reconnects Discord, and remains active. Discord resource denial now retries
in-process with bounded exponential backoff while lifecycle workers remain
available, with a 30-second systemd fallback for actual process crashes.

No normal-user browser automation is used. The final acceptance used real
Discord participants for user1–user3 and approved disposable out-of-band Linux
clients for user4–user5.
