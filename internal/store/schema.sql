PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS slots (
    id TEXT PRIMARY KEY,
    ludus_user TEXT NOT NULL UNIQUE,
    range_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_start TEXT,
    lease_end TEXT,
    wireguard_peer_id TEXT,
    wireguard_issued_at TEXT,
    wireguard_revoked_at TEXT,
    wireguard_first_handshake_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS reservations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    discord_user_id TEXT NOT NULL,
    state TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    assigned_slot TEXT REFERENCES slots(id),
    preferred_slot TEXT REFERENCES slots(id),
    lease_start TEXT,
    lease_end TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS one_active_reservation_per_discord_user
ON reservations(discord_user_id)
WHERE state IN ('QUEUED', 'PREPARING', 'ASSIGNED', 'DRAINING');

CREATE TABLE IF NOT EXISTS cooldowns (
    discord_user_id TEXT PRIMARY KEY,
    next_eligible_at TEXT NOT NULL,
    reason TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    slot_id TEXT NOT NULL REFERENCES slots(id),
    generation INTEGER NOT NULL,
    transition TEXT NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    actor TEXT NOT NULL,
    slot_id TEXT,
    reservation_id INTEGER,
    action TEXT NOT NULL,
    result TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '{}'
);
