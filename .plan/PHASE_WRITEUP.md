# VibeRange broker live-validation plan

## Purpose

This plan governs the transition from a simulation-mode broker to a validated
production lifecycle for five reusable Ludus participant slots (`user1` through
`user5`). It is intentionally independent of Discord interactions. Discord is
an eventual participant transport; it is not part of range reset, WireGuard
peer lifecycle, recovery, or correctness testing.

The WireGuard-only decision in `docs/wireguard-only-delivery.md` supersedes all
prior participant API-key handoff language. Participant Ludus API keys, PAM or
Proxmox credentials, and host SSH credentials must never be delivered. The
approved profile path is a single in-memory ZIP attachment in the initiating
user's private Discord DM. It contains the `.conf`, Linux/Windows guides, and
Bash/PowerShell acceptance scripts; slash-command responses remain ephemeral
status only.

This file is public-safe desired-state documentation. Deployment addresses,
VM identifiers, Discord identifiers, API keys, SSH keys, participant keys,
mutable databases, and raw live evidence remain outside Git.

## Current accepted state

The following foundations are complete:

- a dedicated QEMU management VM exists outside Ludus participant ranges;
- operator SSH is public-key-only and traverses a no-shell, destination-limited
  ProxyJump;
- the VM has persistent SQLite state, NTP, guest firewalling, systemd units,
  and encrypted daily backup;
- an encrypted backup was copied off-VM and restore-tested successfully;
- the broker reaches Ludus over the private management network with API-key
  authentication and an exact TLS leaf-certificate fingerprint;
- live read-only inventory for all five slots returns the matching range ID,
  `SUCCESS`, testing enabled, four VMs, and all VMs powered on;
- the old recording reservations have been removed from range metadata;
- the Discord Gateway broker service is disabled during out-of-band testing;
- the existing test guild remains configured for later human acceptance.

Production mode remains fail-closed until the lifecycle worker and all gates in
this plan are complete.

## Canonical blueprint invariant

Every participant range must be derived from the same immutable release
blueprint, not from a development branch or hand-edited range configuration.
The accepted source is:

```text
source ID:         viberange
blueprint ID:      viberange/viberange
source Git ref:    v0.11.7
blueprint version: 0.11.7
```

The release blueprint has canonical SHA-256
`655ab029cd89eae471fcf328e302d7659ea4937c4002d6a1f33ee71378eb1323`.
Live slot configurations intentionally add a deterministic, slot-specific
`always_blocked_networks` overlay that denies management networks and every
other participant range. They are therefore not byte-identical. The broker
stores an exact approved SHA-256 for every slot in ignored deployment
configuration and fails closed on any unapproved change.

A destructive lifecycle operation must fail closed if:

- source sync status is not successful;
- the source ID or release ref differs;
- embedded blueprint version differs;
- the exact slot-specific approved configuration hash differs;
- the target range identity does not match the requested slot.

Drift is reported and quarantined. The broker must not silently overwrite a
drifted configuration during assignment or teardown.

## State contract

### Clean available slot

A slot is clean and available only when all conditions hold:

- broker state is `AVAILABLE`;
- no active reservation or lease references the slot;
- the authoritative Ludus range ID equals the slot ID;
- range state is `SUCCESS`;
- Ludus testing state is enabled;
- exactly the canonical four VMs are present;
- all required VMs are powered on;
- canonical blueprint identity and hash match;
- readiness probes pass;
- no lifecycle job is ambiguous or running;
- no participant Ludus API credential is active or delivered;
- the previous WireGuard peer/profile has been revoked and proven unusable;
- no uncertain profile delivery remains active.

### Assignment setup

Out-of-band assignment preparation performs:

1. claim the slot generation and durable idempotency key;
2. re-read authoritative inventory and canonical blueprint identity;
3. stop Ludus testing with `force=false`, reverting the existing baseline;
4. power on all range VMs;
5. wait with a bounded timeout for `SUCCESS` and readiness;
6. start Ludus testing, creating the clean assignment baseline;
7. confirm testing is enabled and readiness still passes;
8. ensure no participant Ludus API credential is delivered;
9. revoke the prior generation's WireGuard peer and prove the old profile fails;
10. issue a fresh generation-bound WireGuard profile and non-secret revocation handle;
11. verify the profile reaches only approved participant-facing services;
12. send one private Discord DM ZIP containing the `.conf` and platform setup/acceptance files;
13. move the slot to `ASSIGNED` only after Discord confirms the DM upload.

### Lease teardown

Teardown performs:

1. revoke the generation-bound WireGuard peer and prove the old profile fails;
2. record revocation and invalidate uncertain delivery state;
3. stop experiments and testing with `force=false`;
5. revert the assignment baseline;
6. power on all canonical VMs;
7. wait for `SUCCESS` and readiness;
8. restart testing to establish the next clean baseline;
9. verify blueprint identity, VM inventory, power, testing, and readiness;
10. clear lease fields and increment the slot generation atomically;
11. return the slot to `AVAILABLE` only after verification succeeds.

Any ambiguity transitions the slot to `QUARANTINED`, never `AVAILABLE`.

## Testing boundary

During phases 1–5:

- `viberange-broker.service` remains disabled and stopped;
- no Discord Gateway connection is opened;
- no Discord REST messages, DMs, or synthetic normal-user interactions occur;
- lifecycle operations are invoked through the local CLI/application service on
  the management VM;
- the existing test guild is used only after lifecycle acceptance for a final
  human `/range` and DM presentation check.

Automating a normal Discord account remains prohibited.

## Phase 1 — implementation and preflight

Complete the production lifecycle worker and Discord-free harness before any
range mutation.

Preflight must capture, in ignored private evidence storage:

- UTC time and broker version;
- selected slot and generation;
- queue, lease, and pending-job state;
- authoritative range inventory;
- canonical blueprint source/ref/version/hash;
- testing state and snapshot expectation;
- VM names, power state, and readiness;
- most recent encrypted broker-state backup and successful restore-test result;
- explicit destructive confirmation.

Preflight must reject an active lease, blueprint drift, missing testing baseline,
unexpected VM, failed backup, unsynchronized clock, or unavailable API.

## Phase 2 — one canary lifecycle

Use `user5` as the proposed WireGuard canary because its range is healthy. This
choice is a plan, not implicit authorization to mutate: destructive execution
starts only after the operator confirms the maintenance window.

Canary acceptance requires:

- successful clean setup sequence;
- successful WireGuard peer issuance without profile/private-key logging;
- proof that the previous profile fails;
- proof that the new profile reaches only approved canary services and no management paths;
- private Discord DM attachment delivery and disabled-DM rollback behavior;
- successful teardown and baseline recreation;
- final `SUCCESS`, testing-enabled, four-VM, all-powered inventory;
- broker slot returned to `AVAILABLE` at a new generation;
- no state change in the other four slots;
- bounded timing evidence for each operation.

## Phase 3 — all five slots

After canary acceptance, execute the exact validated lifecycle sequentially for
`user1` through `user5`. Do not run destructive operations concurrently in the
first production validation.

For every slot, capture before/after inventory and prove:

- canonical blueprint parity remains intact;
- setup creates a clean testing baseline;
- teardown removes participant effects;
- old WireGuard profiles no longer connect;
- participant Ludus API keys are never delivered;
- the next baseline is ready;
- no other slot changes generation, lease, testing, or inventory state.

## Phase 4 — crash and replay safety

Inject controlled termination after durable checkpoints, including:

- after job claim but before external mutation;
- after testing stop/revert;
- after power-on but before readiness;
- after WireGuard issuance but before Discord DM confirmation;
- during teardown before baseline recreation;
- after external success but before local job completion.

On restart, prove:

- interrupted jobs are recovered deliberately;
- idempotency keys and generations prevent duplicate mutation;
- a rotated key is never emitted twice;
- uncertain external state is re-read before action;
- ambiguity quarantines the slot;
- operator recovery can restore a verified baseline.

## Phase 5 — evidence and acceptance

Publish only sanitized evidence:

- software versions and blueprint release;
- test names and pass/fail status;
- operation durations;
- slot labels without credentials or infrastructure identifiers;
- final state invariants;
- backup/restore and crash-recovery results.

Never publish raw inventory, addresses, VMIDs, Discord IDs, API responses
containing credentials, SOPS ciphertext, databases, logs with participant IDs,
or deployment-specific configuration.

## Phase 6 — Discord acceptance

Only after lifecycle acceptance:

1. install the bot in the selected server;
2. restrict visibility to the selected test channel;
3. configure a non-managed participant role;
4. enable the broker service in simulation for one human interaction check;
5. validate `/range join`, `/range status`, `/range leave`, and DM delivery;
6. switch to production only after the production fail-closed guard is replaced
   by the accepted worker and all production gates pass.

## Rollback and containment

- Stop the lifecycle worker before manual intervention.
- Preserve the SQLite database and encrypted backup.
- Never mark a partially reset slot available.
- Revoke the WireGuard peer if DM delivery state is uncertain.
- Use authoritative Ludus inventory rather than local assumptions.
- Reapply the canonical blueprint only as an explicit operator recovery action,
  not as automatic drift correction.
- Keep unaffected slots operational while the failed slot remains quarantined.

## Permissions and operator decisions

No additional technical permission is currently required for implementation,
read-only validation, VM administration, backups, or Ludus administration.
The broker VM has the necessary operator and Ludus credentials.

Two operator decisions are required before destructive execution:

1. confirm that the proposed canary slot may be reverted and its WireGuard peer replaced;
2. confirm the maintenance window in which all five participant ranges may be
   sequentially reverted and all five WireGuard profiles replaced.

Target-server Discord installation and channel/role management are deferred and
do not block out-of-band lifecycle work.

## Beads mapping

```text
LVB-3po.5       production Ludus adapter and lifecycle worker
LVB-3po.6       autonomous validation suite
LVB-3po.16      out-of-band live-validation epic
  .16.1         destructive preflight and rollback guardrails
  .16.2         remove obsolete recording metadata (complete)
  .16.3         validate all five setup/teardown cycles
  .16.4         crash recovery and idempotent replay
  .16.5         one canary lifecycle
  .16.6         Discord-free live lifecycle harness
  .16.7         canonical blueprint parity proof (complete)
  .16.8         live key rotation and revocation
  .16.9         maintain this phase writeup
  .16.10        enforce canonical blueprint identity in code
  .16.11        publish sanitized acceptance evidence
LVB-3po.17      generation-bound WireGuard access and private Discord DM attachment
LVB-3po.11      final Discord guild/channel/role configuration
LVB-3po.12      final production acceptance and cutover
```

Critical path:

```text
WireGuard capability proof
  -> plugin + metadata + adapter + private Discord DM attachment
  -> lifecycle integration + secret-leakage tests
  -> preflight + harness + blueprint guard
  -> user5 WireGuard canary
  -> all five slots + peer revocation + crash recovery
  -> sanitized acceptance evidence
  -> final human Discord acceptance
  -> production cutover
```
