# WireGuard-only participant delivery requirement

## Decision

Workshop participants receive only a generation-bound WireGuard client profile.
They must never receive Ludus API keys, Proxmox/PAM passwords, host SSH
credentials, or direct Proxmox access. The broker retains its protected Ludus
administrator credential solely for lifecycle automation.

The profile is delivered inside one ZIP attachment in a private Discord DM from
the bot. The archive also contains generated Linux and Windows setup guides and
bounded Bash and PowerShell acceptance scripts. A separate non-secret Markdown
overview is attached in the same DM so Discord can preview the initial steps. Slash-command acknowledgements, status, and failures remain ephemeral
in the configured workshop channel. Public channel messages never contain the
profile.

## Delivery boundary

A WireGuard configuration contains a client private key and is secret material.
It may exist only in:

- bounded plugin and broker memory while an assignment is prepared;
- the initiating participant's private Discord DM ZIP attachment;
- the participant's downloaded file.

It must not be persisted in SQLite, logs, audit detail, Beads, process arguments,
tracked artifacts, or public/ephemeral channel responses. Discord necessarily
processes the private attachment. Lease-end peer revocation limits the lifetime
of a retained or shared profile.

HTTPS ingress, Discord OAuth, and a separate handoff web service are not needed.
The bot requires no channel `Attach Files` permission for a private DM.

## Lifecycle

### Preparation

For a selected `user1`–`user5` slot, the broker must:

1. revalidate the Discord guild membership and participant role;
2. stop active experiments and restore the approved clean range baseline;
3. power and verify the router plus three workload VMs;
4. ask the root-context Ludus plugin to replace the slot's WireGuard peer with
   a fresh generation-bound keypair;
5. persist only the generation, peer fingerprint, and issue timestamp;
6. build one in-memory ZIP containing the `.conf`, non-secret Linux and Windows
   guides, and generated Bash and PowerShell acceptance scripts, then DM it to
   the initiating Discord user beside a previewable non-secret Markdown overview;
7. commit the lease only after Discord confirms the complete DM upload.

Before mutating a range, the broker sends a private preparation notice to prove
that DMs are available. If that fails, it cancels the reservation without
external mutation. If issue, metadata commit, or final attachment delivery
fails later, the broker revokes the new peer, resets the range, cancels the
reservation for an explicit retry, and quarantines the slot if rollback cannot
be proven.

### Assignment

The DM contains the slot label, expiration, one previewable Markdown overview,
and one slot-scoped ZIP plus its SHA-256 digest. The archive contains the
`.conf`, Linux and Windows guides, and generated Bash and PowerShell acceptance
scripts. Both scripts validate profile metadata, bring up the tunnel, and check
intended participant services. Linux bounded mode tears down and removes the
profile automatically; `--background` leaves the validated interface running
and `--cleanup` later removes it independently. Participant scripts do not probe
intentionally inaccessible networks; server-side isolation remains an
operator acceptance gate. Windows removes its installed tunnel/profile but
leaves the downloaded ZIP/profile for explicit user deletion. Neither guide nor script contains a private
key, Ludus login identity, API key, or participant password.

The ten-minute no-show policy uses the WireGuard peer's latest handshake, not
Ludus API activity. A lease without a post-issue handshake after the configured
grace period is reclaimed through the normal durable revoke/reset job.

### Expiry or early release

1. revoke the exact WireGuard peer generation;
2. record revocation metadata;
3. reset and verify the range;
4. apply the participant cooldown;
5. return the slot to `AVAILABLE` only after revocation and cleanup succeed.

A stale job may not revoke a newer generation. Repeated failures quarantine the
slot.

## Ludus plugin

Ludus creates a user's WireGuard keypair once and otherwise returns the same
profile. The VibeRange admin plugin adds bounded authenticated operations under:

```text
POST /api/v2/user/viberange-wireguard/issue
POST /api/v2/user/viberange-wireguard/revoke
POST /api/v2/user/viberange-wireguard/cleanup
GET  /api/v2/user/viberange-wireguard/status
```

The plugin is loaded by Ludus' native Go `.so` plugin mechanism in the root
admin service. It accepts only `user1`–`user5`, updates the user's marked peer
block atomically, and applies changes with `wg syncconf` without restarting the
interface. Its route is reached through the broker's existing pinned-TLS Ludus
API connection and administrator credential; no broker-to-Ludus SSH path is
added.

The plugin retains only non-secret peer state. It removes the stock per-user
private-key file after issuance so the profile cannot later be fetched through
Ludus' static profile endpoint. Before workload snapshot rollback, the cleanup
operation targets only the matching managed router through the Proxmox guest
agent, stops capture jobs, removes transient run/Zeek/Vector state, restarts
Vector, and fails closed on any ambiguous result.

## Domain and adapter contract

The broker's Ludus boundary provides:

```text
IssueWireGuard(slot, generation) -> transient profile + non-secret peer metadata
RevokeWireGuard(slot, generation) -> revocation confirmation
WireGuardStatus(slot) -> non-secret peer and handshake metadata
```

SQLite stores only:

- peer fingerprint/identifier;
- generation;
- issue and revocation timestamps;
- first observed handshake timestamp;
- normal reservation, lease, and durable-job state.

No plaintext profile column exists.

## Acceptance gates

Before production cutover:

- plugin unit tests and compatible Ludus plugin build pass;
- participant API-key delivery is absent from code and participant messages;
- every slash-command response is ephemeral;
- the `.conf` is packaged only in a single private DM ZIP attachment;
- disabled-DM failure revokes the new peer and safely resets the slot;
- a new profile connects and its predecessor no longer handshakes;
- each profile reaches only its intended workshop range and cannot reach Ludus,
  Proxmox, SSH, SPICE, management, storage, out-of-band, another participant
  range, or unintended tailnet routes;
- expiry, no-show reclamation, restart recovery, retries, and quarantine are
  generation-safe;
- logs, SQLite, backups, diagnostics, and tracked files contain no profile or
  private key.

The plugin is installed and all five slots have passed live profile, handshake,
intended-service, revocation, and cleanup acceptance. Three real Discord users
held simultaneous healthy leases on user1–user3; approved disposable Linux
clients completed Discord-free user4/user5 checks. Operator isolation probes,
router cleanup, and generation reconciliation passed. The broker service and
encrypted-backup timer are enabled across reboot; two consecutive VM reboot
checks confirmed fail-closed startup, automatic retry after early network
unavailability, plugin health, all-slot blueprint approval, Discord
reconnection, and SQLite state.
