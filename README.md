# Ludus VibeRange Broker

Discord-first participant reservation, two-hour lease, generation-bound
WireGuard delivery, and range-turnover automation for reusable Ludus workshop
ranges.

## Status

The broker provides guild-scoped `/range` commands, a SQLite-backed queue and
assignment worker, durable lifecycle jobs, private single-ZIP WireGuard DM
handoffs for Linux and Windows, Discord resource discovery, and live Discord/Ludus diagnostics. The
Ludus 2.3 plugin is installed and live join/status/leave, revocation, router
cleanup, backup/restore, user1 isolation acceptance, and two consecutive VM
reboot recovery checks have passed. The broker and encrypted-backup timer are
enabled across reboot. Three simultaneous Discord leases on user1–user3 and
Discord-free user4/user5 connectivity, service, revocation, and cleanup checks
complete the all-slot acceptance record.

## Participant experience

```text
/range join
/range leave
/range status
```

When a slot is ready, the bot sends the participant a private ZIP containing a
fresh generation-bound WireGuard `.conf`, Linux and Windows setup guides, and
bounded Bash and PowerShell acceptance scripts. A non-secret Markdown overview
is attached beside the ZIP for direct Discord preview. The DM also states the
slot and lease expiration. The peer is revoked at
expiry or after a ten-minute
no-show, then the broker resets and verifies the range before returning it to
the queue. Participants never receive a Ludus API key.

## Instructor experience

Administration stays off Discord. The same binary provides local commands on
the management VM:

```text
vbr slots
vbr participants
vbr cooldowns
vbr queue
vbr assign <discord-user-id> [--slot user3]
vbr extend <discord-user-id> 30m --yes
vbr release <discord-user-id> --yes
vbr thaw <discord-user-id> --yes
vbr reset user3 --yes
vbr quarantine user3 --yes
vbr recover user3 --yes
vbr restore-slot user3 --yes
vbr doctor
vbr selftest
vbr discord discover
vbr ludus inventory user3
```

Read-only commands query SQLite. Mutations insert durable typed jobs that the
running `serve` process executes with its protected Ludus credential.
`restore-slot` is the state-aware instructor operation for revoking the
authoritative current peer, reconciling out-of-band generation drift, restoring
the approved baseline, and returning an aligned slot to `AVAILABLE`; it refuses
in-flight preparation, ambiguous jobs/reservations, or unapproved blueprint
drift.

## Architecture

One Go binary runs on a dedicated management VM. Systemd supervises its
`serve` subcommand while operators invoke local subcommands from the same
binary:

```text
Discord gateway ──outbound──> viberange-broker serve
                                  |-- participant command/DM adapter
local CLI ──SQLite jobs──────────>|-- transactional lease scheduler
                                  |-- idempotent lifecycle worker
                                  |-- Ludus REST/plugin adapter ─────> Ludus
                                  `-- SQLite WAL database
```

No public inbound HTTP service is required. If Discord temporarily denies the
configured channel, the Gateway remains fail-closed and retries resource
validation with bounded exponential backoff while lifecycle workers continue;
systemd uses a slower fallback restart interval for actual process failures.

The core depends on the small `internal/ludus.Client` interface. Standard Ludus
REST endpoints provide inventory/reset operations; a root-context native Ludus
plugin provides bounded WireGuard issue/revoke/status and persistent-router
cleanup routes. Queue, lease, reset, and Discord logic remain independent of
plugin internals.

### Why rotate peers instead of creating temporary Ludus users?

Ludus can natively create a temporary user, grant it access to an existing
range, return its WireGuard profile, and delete the user at lease expiry. That
would avoid a custom WireGuard route, but each lease would also create a PAM
account, Proxmox identity and token, Ludus API key, home directory, pool ACLs,
and an automatic default range/network record. Partial cleanup could leave
privileged identity or infrastructure artifacts behind.

The accepted design keeps the permanent `user1`–`user5` slots and rotates only
their WireGuard peers. Participants receive no Ludus or Proxmox identity, and
teardown has one generation-bound access object to revoke. Ludus 2.3 uses the
narrow native plugin documented here; a future Ludus 2.4 RPC plugin can replace
it behind the same broker interface.

## Slot lifecycle

```text
AVAILABLE -> RESERVED -> PREPARING -> READY -> ASSIGNED
          -> DRAINING -> RESETTING -> VERIFYING -> AVAILABLE

any failed transition -> QUARANTINED
```

Every job carries a slot generation and idempotency key. On startup the broker
reconciles expired and incomplete leases before accepting new queue work.

## Security

- Persistent broker files share one `/var/lib/viberange-broker` tree, with root-owned configuration and credential sources separated from the service-owned `state/` subdirectory.
- Discord and Ludus secrets are staged as systemd credentials at runtime and are never database fields.
- Participants never receive Ludus API keys, PAM passwords, or management credentials.
- A fresh WireGuard peer is issued per assignment generation and revoked before cleanup.
- The full profile and generated ZIP exist only in bounded memory and the participant's private Discord DM attachment; SQLite stores non-secret peer metadata only.
- SQLite, configuration, and credential paths use restrictive service-user
  permissions.
- Participant ranges never host the broker and cannot reset its state.
- Discord exposes only `/range join`, `/range leave`, and `/range status`.
- Eligibility requires exact guild, channel, and non-self-assignable participant
  role IDs, with membership revalidated before WireGuard delivery.
- One active queue entry, offer, or lease is enforced per Discord user, with
  FIFO ordering, claim timeout, fixed lease, and cooldown.
- Slot/range identity is resolved from the Ludus API; static range numbers are
  not trusted.
- Destructive lifecycle work requires an exact approved per-slot configuration
  hash, including each range's slot-specific management/other-range firewall overlay.

## Development

Requirements: Go 1.23 or newer.

```bash
go test ./...
go build ./cmd/viberange-broker
```

Run locally in safe simulation mode:

```bash
mkdir -p .state
VBR_MODE=simulation \
VBR_DATABASE_PATH="$PWD/.state/broker.db" \
VBR_DISCORD_TOKEN_FILE=/secure/path/discord-bot-token \
VBR_DISCORD_GUILD_ID=123456789012345678 \
VBR_DISCORD_CHANNEL_ID=123456789012345678 \
  go run ./cmd/viberange-broker
```

Simulation mode registers `/range join`, `/range leave`, and `/range status`,
assigns an available SQLite slot, and sends a clearly labelled unusable
WireGuard `.conf` by private DM. It does not rotate a real peer or mutate a
Ludus range.

Discover immutable Discord configuration values without guessing role names:

```bash
viberange-broker discord discover
```

Then set `VBR_DISCORD_GUILD_ID`, `VBR_DISCORD_CHANNEL_ID`, and
`VBR_DISCORD_PARTICIPANT_ROLE_ID`. Production mode refuses to start without the
participant role and validates all three resources through Discord at startup.

## Management VM

Recommended initial VM:

- Debian 12;
- 2 vCPU;
- 2–4 GiB RAM;
- 20 GiB persistent disk;
- management network access to Ludus;
- outbound HTTPS/WebSocket access to Discord;
- SSH restricted to operators.

The production filesystem is:

```text
/var/lib/viberange-broker/
├── broker.env                 # root-owned deployment configuration
├── credentials/              # root-owned persistent credential sources
│   ├── discord-token
│   └── ludus-api-key
└── state/                    # owned by the broker service account
    ├── broker.db
    ├── broker.db-wal         # present while SQLite WAL mode is active
    └── broker.db-shm         # WAL coordination metadata
```

See [`docs/architecture.md`](docs/architecture.md) for the complete accepted
design, [`docs/poc-validation.md`](docs/poc-validation.md) for current validation
evidence and production gates, [`docs/ludus-integration.md`](docs/ludus-integration.md)
for the API compatibility strategy,
[`docs/ludus-2.3-native-plugin-rebuild.md`](docs/ludus-2.3-native-plugin-rebuild.md)
for the exact embedded-UI/docs native plugin rebuild procedure, and
[`packaging/systemd/`](packaging/systemd/) for the service unit.
