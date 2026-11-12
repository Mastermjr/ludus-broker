# VibeRange broker architecture

## Status

This document is the accepted architecture for the proof of concept. It records
the security, fairness, transport, state, lifecycle, command, and deployment
decisions that implementation and tests must preserve.

The broker coordinates five reusable Ludus participant slots (`user1` through
`user5`). Participants interact through Discord. Operators administer the
broker locally on its dedicated management VM. The broker resets and verifies a
slot, issues a generation-bound WireGuard peer, delivers its profile by private
Discord DM, expires the lease, and safely returns the slot to the pool.

## Goals

- Give eligible participants a simple `/range` queue.
- Reuse five slots through fixed, automatically enforced leases.
- Remove routine range reset and credential rotation work from instructors.
- Remain safe after crashes, duplicate commands, delayed jobs, and partial
  external failures.
- Keep Discord outside the administrative trust boundary.
- Keep the implementation deployable as one Go binary and one SQLite database.
- Isolate Ludus API details so a supported Ludus 2.4 extension can replace the
  adapter without changing queue or lifecycle logic.

## Non-goals for the PoC

- A public website or public inbound API.
- Entra/OIDC identity federation.
- Discord administrative commands.
- Automation of a normal Discord user account or a Discord self-bot.
- Direct Proxmox control; Ludus remains the infrastructure authority.
- Persisting WireGuard profiles, private keys, participant credentials, passwords, or Discord tokens in SQLite.

## Deployment model

One Go binary provides the continuously running server and local operator CLI:

```text
viberange-broker serve
viberange-broker slots
viberange-broker queue
viberange-broker assign ...
viberange-broker reset ...
```

Systemd runs only the server subcommand:

```ini
ExecStart=/usr/local/bin/viberange-broker serve
```

Systemd is a process supervisor, not a separate application. It starts the
server after reboot, restarts it after failure, provides journald logging, and
makes protected Discord and Ludus credentials available to `serve`.

The initial management VM target is Debian 12 with 2 vCPU, 2–4 GiB RAM, 20 GiB
persistent storage, outbound Discord access, and management-network access to
the Ludus API. Participant ranges never host the broker.

## Trust and network boundaries

```text
Participant Discord client
          |
          | Discord-managed interaction
          v
Discord API and Gateway
          ^
          | outbound HTTPS/WSS only
          |
+----------------------------------------------------+
| Broker management VM                               |
|                                                    |
|  viberange-broker serve                            |
|    |-- Discord adapter                             |
|    |-- command service                             |
|    |-- scheduler/lifecycle worker                  |
|    |-- Ludus REST adapter                          |
|    `-- SQLite WAL database                         |
|                                                    |
|  local operator                                    |
|    `-- viberange-broker <admin subcommand>         |
+----------------------------------------------------+
          |
          | outbound authenticated HTTPS
          v
       Ludus API
          |
          v
       Proxmox
```

The broker exposes no public listener. Discord commands arrive over a
persistent outbound Gateway WebSocket. The broker responds, registers commands,
and sends DMs through Discord HTTPS APIs. The lifecycle worker calls the Ludus
2.x REST API directly; it does not shell out to the Ludus CLI in production and
does not connect directly to Proxmox.

Required management-VM network paths are:

- outbound TCP 443 to Discord API, CDN, and Gateway endpoints;
- outbound TCP 8080 or 443, as deployed, to the Ludus management API;
- DNS and time synchronization appropriate for TLS and lease correctness.

The deployed management VM reaches Ludus directly over its private management
network. Because the current Ludus certificate is expired, the client verifies
an exact SHA-256 leaf-certificate fingerprint instead of using a trust-all TLS
setting. Certificate rotation therefore fails closed until an operator records
the replacement fingerprint. An SSH tunnel remains an optional fallback for
deployments where the private API endpoint is not routable.

Discord WebSocket endpoints must not be broken by TLS interception. If an
egress proxy is mandatory, Discord domains should be tunneled without MITM and
the proxy must support long-lived WebSockets.

## State ownership

Each system owns a different kind of state:

- **Discord** owns guild membership, channel membership, role assignments, and
  interaction delivery.
- **SQLite** owns queue order, offers, leases, slot intent, generations,
  lifecycle jobs, cooldowns, and sanitized audit events.
- **Ludus** owns actual range, VM, snapshot, testing, and WireGuard peer state.
- **Systemd credentials** own the Discord bot token and Ludus administrator API
  key presented to the running service.

Persistent files are consolidated under one deployment root without giving the
service write access to its own credential sources:

```text
/var/lib/viberange-broker/
├── broker.env                 # root-owned deployment configuration
├── credentials/              # root-owned, mode 0700
│   ├── discord-token
│   └── ludus-api-key
└── state/                    # viberange-broker-owned, mode 0700
    ├── broker.db
    ├── broker.db-wal
    └── broker.db-shm
```

Systemd copies the Discord and Ludus API credentials into its protected runtime
credential directory for `serve`. When direct private-network API access is
unavailable, an optional tunnel service can consume a separately generated
forwarding key; the deployed management VM does not need that fallback. The
separate `state/` subdirectory is a permission boundary inside the single deployment
root: SQLite must be writable by the service, while configuration and credential
sources must not be.

SQLite runs in write-ahead logging (WAL) mode. New transactions are appended to
`broker.db-wal` before SQLite checkpoints them into `broker.db`;
`broker.db-shm` coordinates readers and the WAL index. They are normal live
sidecar files, not independent databases. Backups must use SQLite's backup API
or a checkpointed, stopped-service copy rather than copying only `broker.db`
while the service is running.

SQLite is the durable control plane, but it is not allowed to overrule observed
Ludus infrastructure state. The worker reconciles intent with Ludus before and
after every lifecycle operation and after every restart. Ambiguous external
responses fail closed and may quarantine the slot.

## Discord participant surface

Discord exposes exactly one guild-scoped application command with participant
subcommands:

```text
/range join
/range leave
/range status
```

No reset, revoke, assignment override, quarantine, or other administrative
operation is registered in Discord.

- `join` creates one idempotent queue entry for an eligible participant.
- `leave` cancels a queued entry. An active lease enters the normal early
  release and reset path rather than becoming immediately available.
- `status` returns queue position, offer/preparation state, assigned slot, and
  expiration as an ephemeral response.

Discord is a transport and roster gate, not the broker database. The broker
does not derive state by scraping Discord messages.

## Participant eligibility

A participant command is accepted only when all of these checks pass:

1. the interaction is a signed event received through the authenticated bot
   Gateway session;
2. its guild ID exactly matches the configured workshop guild ID;
3. its channel ID exactly matches the configured participant channel ID;
4. its immutable Discord user ID is present;
5. the member has the configured participant role ID;
6. the role is not `@everyone` and is not the bot's own role;
7. the rate limit, cooldown, and one-active-reservation rules pass.

The participant role must be assigned from the attendee roster and must not be
self-assignable. Usernames and role names are display values only; authorization
uses immutable IDs.

Membership and role eligibility are checked again immediately before issuing a
credential. A participant who left the server or lost the role while queued
cannot receive a slot.

Commands from DMs, other guilds, other channels, or ineligible members receive
an ephemeral denial and create a sanitized audit event. Private threads are not
accepted in the PoC; supporting them later requires validating the parent
channel through Discord's API.

## Easy and safe Discord role configuration

The bot can list guilds, channels, and roles it can see, but it cannot safely
infer what a human means by “participant role.” A server may have multiple
roster, attendee, sponsor, instructor, or self-service roles with similar
permissions. Silently choosing one would create an authorization vulnerability.

Configuration therefore uses discovery followed by explicit selection:

```bash
viberange-broker discord discover
viberange-broker configure discord \
  --guild mastermjr \
  --channel viberange-queue \
  --participant-role DEATHCON-Participant
```

Discovery returns names and immutable IDs. Configuration resolves a unique name
once and stores the resulting IDs, not names. It fails on missing or ambiguous
matches and prints the selected guild, channel, and role for confirmation.
Operators may provide IDs directly for automation:

```bash
viberange-broker configure discord \
  --guild-id 123... \
  --channel-id 456... \
  --participant-role-id 789...
```

On every startup, `serve` verifies that the configured guild, channel, and role
still exist and that the bot can use the channel. It refuses new joins if this
validation fails. `viberange-broker doctor` performs the same checks on demand.

Automatic selection is allowed only for a non-production bootstrap when the bot
is installed in exactly one guild and the operator still explicitly confirms
the displayed IDs. The participant role is never silently inferred.

## Fairness and anti-hoarding policy

Discord user IDs alone cannot prevent alternate accounts. The controlled,
non-self-assignable participant role is the primary roster control. SQLite then
enforces fairness for every eligible identity.

PoC policy defaults are:

- one active queue entry, offer, preparation, or lease per Discord user;
- FIFO queue ordering by durable request time and reservation ID;
- a fixed two-hour lease;
- a ten-minute claim window before credentials are issued;
- a configurable 30-minute rejoin cooldown after a completed lease;
- no participant extension while another eligible participant is waiting;
- idempotent duplicate joins and leaves;
- automatic expiration, credential revocation, reset, and verification;
- manual operator override only through the local CLI, with an audit event.

When a slot is ready, the bot sends an offer with a short-lived claim button. A
claim is bound to the Discord user ID, reservation ID, slot generation, and
random nonce. Failure to claim returns the slot to the queue without revealing
a credential. Claim nonces are stored as hashes.

The exact lease, claim, cooldown, and warning durations are configuration, but
changing them does not bypass the uniqueness or authorization constraints.

## Slot lifecycle

```text
AVAILABLE
  -> RESERVED
  -> PREPARING
  -> READY
  -> OFFERED
  -> ASSIGNED
  -> DRAINING
  -> RESETTING
  -> VERIFYING
  -> AVAILABLE

Any unsafe or repeatedly failed transition -> QUARANTINED
```

Every slot has a monotonically increasing generation. Every lifecycle job,
offer, handoff, and audit event carries that generation so a delayed operation
cannot alter a later lease.

A database transaction claims each job. Idempotency keys prevent duplicate
external operations. Different slots may progress concurrently within a
configured worker limit; operations for one slot are serialized.

## Assignment and turnover sequence

A normal assignment performs:

1. select the oldest eligible queued reservation and one available slot;
2. revalidate the participant's Discord membership and role;
3. verify canonical blueprint and authoritative inventory;
4. restore the clean Ludus testing baseline and power all expected VMs;
5. verify range identity, power, testing state, and readiness;
6. issue a fresh generation-bound WireGuard peer through the Ludus plugin;
7. persist only its peer fingerprint and issue timestamp;
8. DM the initiating user the `.conf` attachment and exact expiration;
9. mark the lease assigned only after successful DM upload.

At expiration or early release, access is revoked before cleanup. The slot is
not available until reset and verification both pass. Failure evidence is
sanitized and retained before bounded retry; exhausted retries quarantine the
slot.

## Credential handling

Discord DMs are not end-to-end encrypted. The accepted workshop path uses a
fresh, generation-bound WireGuard profile whose peer is revoked at expiry,
release, no-show reclamation, or failed delivery:

- participants never receive Ludus or Proxmox credentials;
- each assignment replaces the slot's prior WireGuard peer;
- the profile is never placed in channels, SQLite, process arguments, logs, or audit detail;
- the `.conf` is uploaded once as a private DM attachment;
- only peer fingerprint, generation, issue/revoke time, and handshake metadata persist;
- a delivery failure revokes the peer before the range is reused.

The Discord bot token and Ludus administrator API key are files supplied to
`serve` as systemd credentials. Interactive CLI commands do not read either
secret. A CLI mutation inserts a typed job; the server performs the external
operation.

## Local administrative CLI

The same binary provides local administration on the management VM:

```text
viberange-broker slots [--json]
viberange-broker queue [--json]
viberange-broker participant <discord-user-id> [--json]
viberange-broker jobs [--json]
viberange-broker audit [--json]
viberange-broker assign <discord-user-id> [--slot user3]
viberange-broker extend <discord-user-id> 30m
viberange-broker release <discord-user-id> --yes
viberange-broker reset user3 --yes
viberange-broker quarantine user3 --reason <text>
viberange-broker recover user3
viberange-broker retry-job <job-id>
viberange-broker discord discover
viberange-broker configure discord ...
viberange-broker doctor
viberange-broker selftest
viberange-broker version
```

Read-only commands query SQLite. Mutating commands validate their arguments and
insert durable typed jobs. `serve` claims and executes those jobs. The CLI never
accepts a shell fragment, raw Ludus endpoint, or arbitrary Proxmox operation.
Destructive commands require confirmation unless an explicit automation flag is
provided, and all requests include the local operator identity in the audit
record.

## SQLite model

Minimum durable entities are:

- `slots`: Ludus user/range IDs, generation, lifecycle state, lease fields,
  baseline identity, quarantine reason;
- `reservations`: Discord user ID, requested time, state, queue ordering,
  offer and assignment references;
- `jobs`: typed payload, idempotency key, slot generation, state, attempts,
  retry time, sanitized error;
- `offers`: reservation, slot generation, nonce hash, expiry, claimed time;
- `cooldowns`: Discord user ID and next eligible time;
- `audit_events`: time, local or Discord actor, action, target, result, and
  sanitized structured detail.

SQLite runs in WAL mode with foreign keys enabled. Schema migrations are
versioned and transactional. Partial unique indexes enforce one active
reservation per Discord user and one active lease per slot.

## Ludus integration boundary

The application depends on a small typed client interface rather than endpoint
paths throughout the code. Required capabilities include:

- server/API version and capability discovery;
- range and VM inventory;
- generation-bound WireGuard issue, status, and revocation;
- active experiment/capture shutdown;
- clean snapshot restoration and power control;
- readiness verification.

The production adapter uses direct HTTPS requests with the protected
administrator API key. It validates status codes and response schemas, applies
bounded timeouts, and treats unknown outcomes as failures. Contract fixtures
cover the deployed Ludus REST API. A native root-context Ludus plugin implements
only the bounded WireGuard operations behind the same pinned HTTPS endpoint.

## Recovery and reconciliation

On startup, `serve` performs migrations and reconciliation before accepting new
joins:

1. recover jobs left `RUNNING` by a crash;
2. process expired offers and leases;
3. compare non-available slots with Ludus inventory;
4. retry safe idempotent work;
5. quarantine ambiguous or unsafe slots;
6. verify Discord configuration and command registration;
7. begin normal queue dispatch.

The server also reconciles periodically. Time is UTC. Startup fails closed when
clock skew exceeds policy because Discord TLS, claim expiry, and leases depend
on correct time.

## Testing strategy

Application semantics are independent of Discord. The Discord adapter parses an
interaction into an actor and typed command, calls the application service, and
renders a response. Tests invoke the application service directly.

Required layers are:

1. **Unit tests:** state transitions, authorization, fairness, policy, command
   results, formatting, and secret redaction.
2. **SQLite integration tests:** migrations, constraints, FIFO ordering,
   concurrent joins, job claiming, idempotency, cooldowns, and restart recovery
   against a real temporary database.
3. **Discord adapter tests:** guild/channel/role extraction, DM rejection,
   malformed interactions, ephemeral responses, and panic containment using
   fixtures without a normal-user token.
4. **Live Discord self-test:** bot authentication, Gateway connection, guild,
   channel, role discovery, command registration, and test-channel delivery.
5. **Ludus contract tests:** recorded sanitized response fixtures, timeouts,
   ambiguous responses, retry classification, and no-secret logging.
6. **Disposable-slot integration:** read inventory, reset, issue/revoke a test
   WireGuard generation, verify stale-profile rejection, retry safely, and quarantine a forced failure.
7. **Operational tests:** process crash, reboot, expired lease while offline,
   corrupted configuration, unavailable Discord, unavailable Ludus, and backup
   and restore.

Automating a normal Discord user account is excluded. Discord does not expose a
supported API for a bot to invoke its own slash command. One final human
acceptance invocation validates Discord-client interaction delivery and DM
presentation; all downstream behavior is covered autonomously.

`viberange-broker selftest` runs bounded, non-destructive checks and emits a
machine-readable result. Production-altering integration tests require an
explicit disposable slot and confirmation flag.

## Security properties

- No public inbound service is required.
- Discord never exposes administrative lifecycle operations.
- A bot-token compromise does not provide the Ludus administrator key.
- Every Discord action is bound to immutable guild, channel, role, and user IDs.
- Every destructive lifecycle action is typed, generation-bound, idempotent,
  locally requested, and audited.
- External inputs never become shell commands.
- Secrets are redacted at logger and error boundaries.
- Service filesystem, process, and network access are restricted by systemd.
- A slot fails closed and quarantines rather than serving an unverified range.

## Deferred decisions

- Whether a future deployment should offer a non-Discord profile-delivery option.
- How to package/rebuild the native plugin across future Ludus and Go upgrades.
