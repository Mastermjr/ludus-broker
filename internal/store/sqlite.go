package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sqlite: %w", err)
	}
	// Recreate the partial index for databases initialized before PREPARING was
	// introduced. This remains idempotent and prevents duplicate active entries
	// while an external range is being prepared.
	if _, err := db.Exec(`
		DROP INDEX IF EXISTS one_active_reservation_per_discord_user;
		CREATE UNIQUE INDEX one_active_reservation_per_discord_user
		ON reservations(discord_user_id)
		WHERE state IN ('QUEUED', 'PREPARING', 'ASSIGNED', 'DRAINING');`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate active reservation index: %w", err)
	}
	if err := addColumnIfMissing(db, "reservations", "preferred_slot", "TEXT REFERENCES slots(id)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate reservations.preferred_slot: %w", err)
	}
	for _, migration := range []struct{ name, definition string }{
		{"wireguard_peer_id", "TEXT"},
		{"wireguard_issued_at", "TEXT"},
		{"wireguard_revoked_at", "TEXT"},
		{"wireguard_first_handshake_at", "TEXT"},
	} {
		if err := addColumnIfMissing(db, "slots", migration.name, migration.definition); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate slots.%s: %w", migration.name, err)
		}
	}
	return &Store{db: db}, nil
}

func addColumnIfMissing(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SeedSlots(ctx context.Context, ids []string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO slots(id, ludus_user, range_id, state, updated_at)
			VALUES(?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING`, id, id, id, domain.SlotAvailable, now)
		if err != nil {
			return fmt.Errorf("seed slot %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func (s *Store) CooldownUntil(ctx context.Context, discordUserID string) (*time.Time, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT next_eligible_at FROM cooldowns WHERE discord_user_id = ?`, discordUserID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Store) SetCooldown(ctx context.Context, discordUserID, reason string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cooldowns(discord_user_id, next_eligible_at, reason) VALUES(?, ?, ?)
		ON CONFLICT(discord_user_id) DO UPDATE SET next_eligible_at = excluded.next_eligible_at,
		reason = excluded.reason`, discordUserID, until.UTC().Format(time.RFC3339Nano), reason)
	return err
}

func (s *Store) ListActiveCooldowns(ctx context.Context, now time.Time) ([]domain.Cooldown, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT discord_user_id, next_eligible_at, reason
		FROM cooldowns WHERE next_eligible_at > ? ORDER BY next_eligible_at`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.Cooldown
	for rows.Next() {
		var value domain.Cooldown
		var until string
		if err := rows.Scan(&value.DiscordUserID, &until, &value.Reason); err != nil {
			return nil, err
		}
		value.Until, err = time.Parse(time.RFC3339Nano, until)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ClearCooldown(ctx context.Context, discordUserID string) error {
	if discordUserID == "" {
		return errors.New("Discord user ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM cooldowns WHERE discord_user_id = ?`, discordUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrNotFound
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, action, result, detail)
		VALUES(?, 'operator', 'cooldown_clear', 'succeeded', json_object('discord_user_id', ?))`, now, discordUserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Enqueue(ctx context.Context, discordUserID string) (int64, error) {
	return s.EnqueuePreferred(ctx, discordUserID, "")
}

func (s *Store) EnqueuePreferred(ctx context.Context, discordUserID, preferredSlot string) (int64, error) {
	if discordUserID == "" {
		return 0, errors.New("Discord user ID is required")
	}
	var preferred any
	if preferredSlot != "" {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT count() FROM slots WHERE id = ?`, preferredSlot).Scan(&exists); err != nil {
			return 0, err
		}
		if exists != 1 {
			return 0, fmt.Errorf("preferred slot %s: %w", preferredSlot, ErrNotFound)
		}
		preferred = preferredSlot
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO reservations(discord_user_id, state, requested_at, preferred_slot)
		VALUES(?, 'QUEUED', ?, ?)`, discordUserID, time.Now().UTC().Format(time.RFC3339Nano), preferred)
	if err != nil {
		return 0, fmt.Errorf("enqueue participant: %w", err)
	}
	return result.LastInsertId()
}

func (s *Store) SlotCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count() FROM slots`).Scan(&count)
	return count, err
}

func (s *Store) SlotState(ctx context.Context, slotID string) (domain.SlotState, error) {
	if slotID == "" {
		return "", errors.New("slot ID is required")
	}
	var state domain.SlotState
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM slots WHERE id = ?`, slotID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return state, nil
}

func (s *Store) ListSlots(ctx context.Context) ([]domain.Slot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ludus_user, range_id, state, generation, lease_owner,
		lease_start, lease_end, wireguard_peer_id, wireguard_issued_at,
		wireguard_revoked_at, wireguard_first_handshake_at, updated_at FROM slots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []domain.Slot
	for rows.Next() {
		var slot domain.Slot
		var owner, start, end, peerID, issued, revoked, handshake sql.NullString
		var updated string
		if err := rows.Scan(&slot.ID, &slot.LudusUser, &slot.RangeID, &slot.State,
			&slot.Generation, &owner, &start, &end, &peerID, &issued, &revoked, &handshake, &updated); err != nil {
			return nil, err
		}
		slot.LeaseOwner = owner.String
		slot.WireGuardPeerID = peerID.String
		if issued.Valid {
			value, err := time.Parse(time.RFC3339Nano, issued.String)
			if err != nil {
				return nil, err
			}
			slot.WireGuardIssuedAt = &value
		}
		if revoked.Valid {
			value, err := time.Parse(time.RFC3339Nano, revoked.String)
			if err != nil {
				return nil, err
			}
			slot.WireGuardRevokedAt = &value
		}
		if handshake.Valid {
			value, err := time.Parse(time.RFC3339Nano, handshake.String)
			if err != nil {
				return nil, err
			}
			slot.WireGuardFirstHandshake = &value
		}
		if start.Valid {
			value, err := time.Parse(time.RFC3339Nano, start.String)
			if err != nil {
				return nil, err
			}
			slot.LeaseStart = &value
		}
		if end.Valid {
			value, err := time.Parse(time.RFC3339Nano, end.String)
			if err != nil {
				return nil, err
			}
			slot.LeaseEnd = &value
		}
		slot.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

func (s *Store) ListReservations(ctx context.Context) ([]domain.Reservation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, discord_user_id, state, requested_at, assigned_slot, preferred_slot, lease_start, lease_end
		FROM reservations WHERE state IN ('QUEUED', 'PREPARING', 'ASSIGNED', 'DRAINING') ORDER BY requested_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reservations []domain.Reservation
	position := 0
	for rows.Next() {
		var reservation domain.Reservation
		var requested string
		var slot, preferred, start, end sql.NullString
		if err := rows.Scan(&reservation.ID, &reservation.DiscordUserID, &reservation.State,
			&requested, &slot, &preferred, &start, &end); err != nil {
			return nil, err
		}
		reservation.RequestedAt, err = time.Parse(time.RFC3339Nano, requested)
		if err != nil {
			return nil, err
		}
		reservation.AssignedSlot = slot.String
		reservation.PreferredSlot = preferred.String
		if reservation.State == "QUEUED" {
			position++
			reservation.QueuePosition = position
		}
		if start.Valid {
			value, err := time.Parse(time.RFC3339Nano, start.String)
			if err != nil {
				return nil, err
			}
			reservation.LeaseStart = &value
		}
		if end.Valid {
			value, err := time.Parse(time.RFC3339Nano, end.String)
			if err != nil {
				return nil, err
			}
			reservation.LeaseEnd = &value
		}
		reservations = append(reservations, reservation)
	}
	return reservations, rows.Err()
}

func (s *Store) ReconcileAvailableGeneration(ctx context.Context, slotID string, generation int64) error {
	if slotID == "" || generation < 0 {
		return errors.New("slot and non-negative generation are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	var state domain.SlotState
	var owner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT generation, state, lease_owner FROM slots WHERE id = ?`, slotID).Scan(&current, &state, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != domain.SlotAvailable || owner.Valid {
		return errors.New("generation reconciliation requires an unowned AVAILABLE slot")
	}
	if generation < current {
		return fmt.Errorf("refusing to decrease slot generation from %d to %d", current, generation)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count() FROM reservations
		WHERE assigned_slot = ? AND state IN ('PREPARING', 'ASSIGNED', 'DRAINING')`, slotID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("generation reconciliation requires no active reservation")
	}
	if generation == current {
		return tx.Commit()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE slots SET generation = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ?`, generation, now, slotID, current, domain.SlotAvailable); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, action, result, detail)
		VALUES(?, 'operator', ?, 'generation_reconcile', 'succeeded',
		json_object('from', ?, 'to', ?))`, now, slotID, current, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateRestoreSlotJob(ctx context.Context, slotID string, generation int64, peerID string, issuedAt time.Time, revokedAt *time.Time) (int64, error) {
	if slotID == "" || generation <= 0 || peerID == "" || issuedAt.IsZero() {
		return 0, errors.New("slot, positive generation, peer ID, and issue time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current int64
	var state domain.SlotState
	var owner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT generation, state, lease_owner FROM slots WHERE id = ?`, slotID).Scan(&current, &state, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	switch state {
	case domain.SlotAvailable, domain.SlotAssigned, domain.SlotDraining, domain.SlotQuarantined:
	default:
		return 0, fmt.Errorf("restore-slot refuses slot %s while state is %s", slotID, state)
	}
	if generation < current {
		return 0, fmt.Errorf("refusing plugin generation %d older than broker generation %d", generation, current)
	}
	var activeJobs int
	if err := tx.QueryRowContext(ctx, `SELECT count() FROM jobs
		WHERE slot_id = ? AND state IN ('PENDING', 'RUNNING')`, slotID).Scan(&activeJobs); err != nil {
		return 0, err
	}
	if activeJobs != 0 {
		return 0, errors.New("restore-slot requires no active lifecycle job")
	}
	var activeReservations int
	if err := tx.QueryRowContext(ctx, `SELECT count() FROM reservations
		WHERE assigned_slot = ? AND state IN ('PREPARING', 'ASSIGNED', 'DRAINING')`, slotID).Scan(&activeReservations); err != nil {
		return 0, err
	}
	if activeReservations > 1 || activeReservations == 1 && (!owner.Valid || owner.String == "") {
		return 0, errors.New("restore-slot found ambiguous active reservation state")
	}
	if activeReservations == 0 && owner.Valid && owner.String != "" {
		return 0, errors.New("restore-slot found a lease owner without an active reservation")
	}
	if activeReservations == 1 {
		var matching int
		if err := tx.QueryRowContext(ctx, `SELECT count() FROM reservations
			WHERE assigned_slot = ? AND discord_user_id = ?
			AND state IN ('ASSIGNED', 'DRAINING')`, slotID, owner.String).Scan(&matching); err != nil {
			return 0, err
		}
		if matching != 1 {
			return 0, errors.New("restore-slot reservation does not match the lease owner")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var revoked any
	if revokedAt != nil {
		revoked = revokedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `UPDATE slots SET generation = ?, wireguard_peer_id = ?,
		wireguard_issued_at = ?, wireguard_revoked_at = ?, wireguard_first_handshake_at = NULL,
		updated_at = ? WHERE id = ? AND generation = ?`, generation, peerID,
		issuedAt.UTC().Format(time.RFC3339Nano), revoked, now, slotID, current)
	if err != nil {
		return 0, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("slot changed while creating restore job")
	}
	key := fmt.Sprintf("RESET:%s:%d", slotID, generation)
	var jobID int64
	var priorState string
	err = tx.QueryRowContext(ctx, `SELECT id, state FROM jobs WHERE idempotency_key = ?`, key).Scan(&jobID, &priorState)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, err = tx.ExecContext(ctx, `INSERT INTO jobs(idempotency_key, slot_id, generation,
			transition, state, attempts, next_attempt_at, created_at, updated_at)
			VALUES(?, ?, ?, 'RESET', 'PENDING', 0, ?, ?, ?)`, key, slotID, generation, now, now, now)
		if err != nil {
			return 0, err
		}
		jobID, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	case priorState == "FAILED":
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'PENDING', attempts = 0,
			next_attempt_at = ?, last_error = NULL, updated_at = ? WHERE id = ? AND state = 'FAILED'`,
			now, now, jobID); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("restore job for generation %d already exists in state %s", generation, priorState)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, action, result, detail)
		VALUES(?, 'operator', ?, 'restore_slot', 'succeeded',
		json_object('from_generation', ?, 'to_generation', ?, 'prior_state', ?, 'job_id', ?))`,
		now, slotID, current, generation, state, jobID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return jobID, nil
}

func (s *Store) CreateWireGuardDriftRecoveryJob(ctx context.Context, slotID string, generation int64, peerID string, issuedAt, revokedAt time.Time) (int64, error) {
	if slotID == "" || generation <= 0 || peerID == "" || issuedAt.IsZero() || revokedAt.IsZero() {
		return 0, errors.New("slot, positive generation, peer ID, issue time, and revocation time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current int64
	var state domain.SlotState
	var owner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT generation, state, lease_owner FROM slots WHERE id = ?`, slotID).Scan(&current, &state, &owner); err != nil {
		return 0, err
	}
	if state != domain.SlotQuarantined || !owner.Valid || owner.String == "" {
		return 0, errors.New("WireGuard drift recovery requires an owned QUARANTINED slot")
	}
	if generation <= current {
		return 0, fmt.Errorf("recovery generation %d must be newer than broker generation %d", generation, current)
	}
	var draining, activeJobs int
	if err := tx.QueryRowContext(ctx, `SELECT count() FROM reservations
		WHERE assigned_slot = ? AND discord_user_id = ? AND state = 'DRAINING'`, slotID, owner.String).Scan(&draining); err != nil {
		return 0, err
	}
	if draining != 1 {
		return 0, errors.New("WireGuard drift recovery requires exactly one matching DRAINING reservation")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count() FROM jobs
		WHERE slot_id = ? AND state IN ('PENDING', 'RUNNING')`, slotID).Scan(&activeJobs); err != nil {
		return 0, err
	}
	if activeJobs != 0 {
		return 0, errors.New("WireGuard drift recovery requires no active lifecycle job")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE slots SET generation = ?, wireguard_peer_id = ?,
		wireguard_issued_at = ?, wireguard_revoked_at = ?, wireguard_first_handshake_at = NULL,
		updated_at = ? WHERE id = ? AND generation = ? AND state = ?`, generation, peerID,
		issuedAt.UTC().Format(time.RFC3339Nano), revokedAt.UTC().Format(time.RFC3339Nano), now,
		slotID, current, domain.SlotQuarantined)
	if err != nil {
		return 0, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("slot changed during WireGuard drift recovery")
	}
	key := fmt.Sprintf("RESET:%s:%d", slotID, generation)
	result, err = tx.ExecContext(ctx, `INSERT INTO jobs(idempotency_key, slot_id, generation,
		transition, state, attempts, next_attempt_at, created_at, updated_at)
		VALUES(?, ?, ?, 'RESET', 'PENDING', 0, ?, ?, ?)`, key, slotID, generation, now, now, now)
	if err != nil {
		return 0, err
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, action, result, detail)
		VALUES(?, 'operator', ?, 'wireguard_drift_recovery', 'succeeded',
		json_object('from_generation', ?, 'to_generation', ?, 'job_id', ?))`,
		now, slotID, current, generation, jobID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return jobID, nil
}

func (s *Store) CreateSlotJob(ctx context.Context, transition, slotID string) (int64, bool, error) {
	allowed := map[string]bool{"RESET": true, "QUARANTINE": true, "RECOVER": true}
	if !allowed[transition] {
		return 0, false, fmt.Errorf("unsupported job transition %q", transition)
	}
	var generation int64
	if err := s.db.QueryRowContext(ctx, `SELECT generation FROM slots WHERE id = ?`, slotID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, ErrNotFound
		}
		return 0, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	key := fmt.Sprintf("%s:%s:%d", transition, slotID, generation)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs(idempotency_key, slot_id, generation, transition, state,
		next_attempt_at, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'PENDING', ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`, key, slotID, generation, transition, now, now, now)
	if err != nil {
		return 0, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM jobs WHERE idempotency_key = ?`, key).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, created > 0, nil
}

func (s *Store) ListJobs(ctx context.Context) ([]domain.Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, idempotency_key, slot_id, generation, transition, state,
		attempts, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
		FROM jobs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []domain.Job
	for rows.Next() {
		var job domain.Job
		var next, created, updated string
		if err := rows.Scan(&job.ID, &job.IdempotencyKey, &job.SlotID, &job.Generation,
			&job.Transition, &job.State, &job.Attempts, &next, &job.LastError, &created, &updated); err != nil {
			return nil, err
		}
		job.NextAttemptAt, err = time.Parse(time.RFC3339Nano, next)
		if err != nil {
			return nil, err
		}
		job.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		job.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) RecoverInterruptedJobs(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET state = 'PENDING', last_error = 'recovered after broker restart',
		next_attempt_at = ?, updated_at = ? WHERE state = 'RUNNING'`, now, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ClaimNextJob(ctx context.Context) (*domain.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var job domain.Job
	var next, created, updated string
	err = tx.QueryRowContext(ctx, `
		SELECT id, idempotency_key, slot_id, generation, transition, state,
		attempts, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
		FROM jobs WHERE state = 'PENDING' AND next_attempt_at <= ? ORDER BY id LIMIT 1`, now).Scan(
		&job.ID, &job.IdempotencyKey, &job.SlotID, &job.Generation, &job.Transition,
		&job.State, &job.Attempts, &next, &job.LastError, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE jobs SET state = 'RUNNING', attempts = attempts + 1, updated_at = ?
		WHERE id = ? AND state = 'PENDING'`, now, job.ID)
	if err != nil {
		return nil, err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if claimed != 1 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.State = "RUNNING"
	job.Attempts++
	job.NextAttemptAt, err = time.Parse(time.RFC3339Nano, next)
	if err != nil {
		return nil, err
	}
	job.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return nil, err
	}
	job.UpdatedAt, err = time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) completeJob(ctx context.Context, job domain.Job, cooldown time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	var leaseOwner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT lease_owner FROM slots WHERE id = ?`, job.SlotID).Scan(&leaseOwner); err != nil {
		return err
	}
	var result sql.Result
	switch job.Transition {
	case "RESET":
		if _, err := tx.ExecContext(ctx, `
			UPDATE reservations SET state = 'COMPLETED'
			WHERE assigned_slot = ? AND state IN ('ASSIGNED', 'DRAINING')`, job.SlotID); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE slots SET state = ?, generation = generation + 1, lease_owner = NULL,
			lease_start = NULL, lease_end = NULL,
			wireguard_revoked_at = COALESCE(wireguard_revoked_at, ?), updated_at = ?
			WHERE id = ? AND generation = ?`, domain.SlotAvailable, now, now, job.SlotID, job.Generation)
	case "QUARANTINE":
		result, err = tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
			WHERE id = ? AND generation = ?`, domain.SlotQuarantined, now, job.SlotID, job.Generation)
	case "RECOVER":
		result, err = tx.ExecContext(ctx, `UPDATE slots SET state = ?, generation = generation + 1,
			updated_at = ? WHERE id = ? AND generation = ? AND state = ?`, domain.SlotAvailable,
			now, job.SlotID, job.Generation, domain.SlotQuarantined)
	default:
		return fmt.Errorf("unsupported transition %q", job.Transition)
	}
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("slot generation or state changed before job execution")
	}
	if job.Transition == "RESET" && cooldown > 0 && leaseOwner.Valid && leaseOwner.String != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cooldowns(discord_user_id, next_eligible_at, reason) VALUES(?, ?, 'completed lease')
			ON CONFLICT(discord_user_id) DO UPDATE SET next_eligible_at = excluded.next_eligible_at,
			reason = excluded.reason`, leaseOwner.String, nowTime.Add(cooldown).Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'SUCCEEDED', updated_at = ?
		WHERE id = ? AND state = 'RUNNING'`, now, job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteJob(ctx context.Context, job domain.Job) error {
	return s.completeJob(ctx, job, 0)
}

func (s *Store) CompleteProductionJob(ctx context.Context, job domain.Job, cooldown time.Duration) error {
	return s.completeJob(ctx, job, cooldown)
}

func (s *Store) CompleteSimulationJob(ctx context.Context, job domain.Job) error {
	return s.completeJob(ctx, job, 0)
}

func (s *Store) FailJob(ctx context.Context, id int64, failure string) error {
	if len(failure) > 500 {
		failure = failure[:500]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'FAILED', last_error = ?, updated_at = ?
		WHERE id = ? AND state = 'RUNNING'`, failure, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) RetryJob(ctx context.Context, id int64, failure string, delay time.Duration) error {
	if len(failure) > 500 {
		failure = failure[:500]
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET state = 'PENDING', last_error = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ? AND state = 'RUNNING'`, failure, now.Add(delay).Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("retry job %d: concurrent state change", id)
	}
	return nil
}

func (s *Store) FailJobAndQuarantine(ctx context.Context, job domain.Job, failure string) error {
	if len(failure) > 500 {
		failure = failure[:500]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'FAILED', last_error = ?, updated_at = ?
		WHERE id = ? AND state = 'RUNNING'`, failure, now, job.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("fail job %d: concurrent state change", job.ID)
	}
	result, err = tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
		WHERE id = ? AND generation = ?`, domain.SlotQuarantined, now, job.SlotID, job.Generation)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("quarantine slot %s: generation changed", job.SlotID)
	}
	return tx.Commit()
}

func (s *Store) ActiveReservation(ctx context.Context, discordUserID string) (domain.Reservation, error) {
	var reservation domain.Reservation
	var requestedAt string
	var assignedSlot sql.NullString
	var preferredSlot sql.NullString
	var leaseStart sql.NullString
	var leaseEnd sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, discord_user_id, state, requested_at, assigned_slot, preferred_slot, lease_start, lease_end
		FROM reservations
		WHERE discord_user_id = ? AND state IN ('QUEUED', 'PREPARING', 'ASSIGNED', 'DRAINING')
		ORDER BY id DESC LIMIT 1`, discordUserID).Scan(
		&reservation.ID, &reservation.DiscordUserID, &reservation.State, &requestedAt,
		&assignedSlot, &preferredSlot, &leaseStart, &leaseEnd,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Reservation{}, ErrNotFound
	}
	if err != nil {
		return domain.Reservation{}, err
	}
	reservation.RequestedAt, err = time.Parse(time.RFC3339Nano, requestedAt)
	if err != nil {
		return domain.Reservation{}, err
	}
	reservation.AssignedSlot = assignedSlot.String
	reservation.PreferredSlot = preferredSlot.String
	if leaseStart.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, leaseStart.String)
		if parseErr != nil {
			return domain.Reservation{}, parseErr
		}
		reservation.LeaseStart = &parsed
	}
	if leaseEnd.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, leaseEnd.String)
		if parseErr != nil {
			return domain.Reservation{}, parseErr
		}
		reservation.LeaseEnd = &parsed
	}
	if reservation.State == "QUEUED" {
		err = s.db.QueryRowContext(ctx, `
			SELECT count() FROM reservations
			WHERE state = 'QUEUED' AND (requested_at < ? OR (requested_at = ? AND id <= ?))`,
			requestedAt, requestedAt, reservation.ID).Scan(&reservation.QueuePosition)
		if err != nil {
			return domain.Reservation{}, err
		}
	}
	return reservation, nil
}

func (s *Store) CancelQueued(ctx context.Context, discordUserID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE reservations SET state = 'CANCELLED'
		WHERE discord_user_id = ? AND state = 'QUEUED'`, discordUserID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) PrepareNext(ctx context.Context) (*domain.Assignment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	assignment := domain.Assignment{}
	var preferred sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT r.id, r.discord_user_id, r.preferred_slot FROM reservations r
		WHERE r.state = 'QUEUED' AND (r.preferred_slot IS NULL OR EXISTS (
			SELECT 1 FROM slots s WHERE s.id = r.preferred_slot AND s.state = ?))
		ORDER BY r.requested_at, r.id LIMIT 1`, domain.SlotAvailable).Scan(
		&assignment.ReservationID, &assignment.DiscordUserID, &preferred,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var slotID string
	var generation int64
	if preferred.Valid {
		err = tx.QueryRowContext(ctx, `SELECT id, generation FROM slots WHERE id = ? AND state = ?`,
			preferred.String, domain.SlotAvailable).Scan(&slotID, &generation)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id, generation FROM slots WHERE state = ? ORDER BY id LIMIT 1`,
			domain.SlotAvailable).Scan(&slotID, &generation)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	assignment.SlotID = slotID
	assignment.Generation = generation + 1
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, generation = generation + 1, lease_owner = ?,
		lease_start = NULL, lease_end = NULL, updated_at = ?
		WHERE id = ? AND state = ? AND generation = ?`,
		domain.SlotPreparing, assignment.DiscordUserID, now, slotID, domain.SlotAvailable, generation)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, ErrNotFound
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'PREPARING', assigned_slot = ?, lease_start = NULL, lease_end = NULL
		WHERE id = ? AND state = 'QUEUED'`, slotID, assignment.ReservationID)
	if err != nil {
		return nil, err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (s *Store) RecordWireGuardIssued(ctx context.Context, assignment domain.Assignment, peerID string, issuedAt time.Time) error {
	if peerID == "" || issuedAt.IsZero() {
		return errors.New("WireGuard peer ID and issue time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE slots SET wireguard_peer_id = ?, wireguard_issued_at = ?, wireguard_revoked_at = NULL,
		wireguard_first_handshake_at = NULL, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ? AND lease_owner = ?`,
		peerID, issuedAt.UTC().Format(time.RFC3339Nano), now,
		assignment.SlotID, assignment.Generation, domain.SlotPreparing, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("record WireGuard issue: concurrent state change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, reservation_id, action, result, detail)
		VALUES(?, 'broker', ?, ?, 'wireguard_issue', 'succeeded', json_object('generation', ?))`,
		now, assignment.SlotID, assignment.ReservationID, assignment.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordWireGuardRevoked(ctx context.Context, slotID string, generation int64, revokedAt time.Time) error {
	if slotID == "" || generation < 0 || revokedAt.IsZero() {
		return errors.New("slot, generation, and revocation time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE slots SET wireguard_revoked_at = ?, updated_at = ?
		WHERE id = ? AND generation = ?`, revokedAt.UTC().Format(time.RFC3339Nano), now, slotID, generation)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("record WireGuard revocation: concurrent generation change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, action, result, detail)
		VALUES(?, 'broker', ?, 'wireguard_revoke', 'succeeded', json_object('generation', ?))`,
		now, slotID, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NoShowCandidates(ctx context.Context, leaseStartedBefore time.Time) ([]domain.Slot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, generation, wireguard_peer_id, wireguard_issued_at
		FROM slots WHERE state = ? AND lease_start <= ?
		AND wireguard_peer_id IS NOT NULL AND wireguard_issued_at IS NOT NULL
		AND wireguard_revoked_at IS NULL AND wireguard_first_handshake_at IS NULL
		ORDER BY id`, domain.SlotAssigned, leaseStartedBefore.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []domain.Slot
	for rows.Next() {
		var slot domain.Slot
		var issued string
		if err := rows.Scan(&slot.ID, &slot.Generation, &slot.WireGuardPeerID, &issued); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, issued)
		if err != nil {
			return nil, err
		}
		slot.WireGuardIssuedAt = &parsed
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

func (s *Store) RecordWireGuardHandshake(ctx context.Context, slotID string, generation int64, handshake time.Time) error {
	if slotID == "" || generation <= 0 || handshake.IsZero() {
		return errors.New("slot, positive generation, and handshake time are required")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE slots SET wireguard_first_handshake_at = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ? AND wireguard_revoked_at IS NULL`,
		handshake.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
		slotID, generation, domain.SlotAssigned)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("record WireGuard handshake: concurrent state change")
	}
	return nil
}

func (s *Store) CompletePreparation(ctx context.Context, assignment domain.Assignment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start := assignment.LeaseStart.UTC().Format(time.RFC3339Nano)
	end := assignment.LeaseEnd.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'ASSIGNED', lease_start = ?, lease_end = ?
		WHERE id = ? AND discord_user_id = ? AND assigned_slot = ? AND state = 'PREPARING'`,
		start, end, assignment.ReservationID, assignment.DiscordUserID, assignment.SlotID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("complete reservation preparation: concurrent state change")
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_start = ?, lease_end = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ? AND lease_owner = ?
		AND wireguard_peer_id IS NOT NULL AND wireguard_issued_at IS NOT NULL
		AND wireguard_revoked_at IS NULL`,
		domain.SlotAssigned, start, end, start, assignment.SlotID, assignment.Generation,
		domain.SlotPreparing, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("complete slot preparation: concurrent state change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, reservation_id, action, result, detail)
		VALUES(?, ?, ?, ?, 'wireguard_dm_delivery', 'succeeded', json_object('generation', ?))`,
		start, assignment.DiscordUserID, assignment.SlotID, assignment.ReservationID, assignment.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EndPreparation(ctx context.Context, assignment domain.Assignment, reservationState string, slotState domain.SlotState) error {
	if reservationState != "QUEUED" && reservationState != "CANCELLED" {
		return fmt.Errorf("unsupported preparation reservation state %q", reservationState)
	}
	if slotState != domain.SlotAvailable && slotState != domain.SlotQuarantined {
		return fmt.Errorf("unsupported preparation slot state %q", slotState)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = ?, assigned_slot = NULL, lease_start = NULL, lease_end = NULL
		WHERE id = ? AND discord_user_id = ? AND state = 'PREPARING'`,
		reservationState, assignment.ReservationID, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("end reservation preparation: concurrent state change")
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_owner = NULL, lease_start = NULL, lease_end = NULL, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ? AND lease_owner = ?`,
		slotState, time.Now().UTC().Format(time.RFC3339Nano), assignment.SlotID,
		assignment.Generation, domain.SlotPreparing, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("end slot preparation: concurrent state change")
	}
	return tx.Commit()
}

func (s *Store) RecoverInterruptedPreparations(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT id, generation FROM slots WHERE state = ?`, domain.SlotPreparing)
	if err != nil {
		return 0, err
	}
	type interruptedSlot struct {
		id         string
		generation int64
	}
	var interrupted []interruptedSlot
	for rows.Next() {
		var slot interruptedSlot
		if err := rows.Scan(&slot.id, &slot.generation); err != nil {
			rows.Close()
			return 0, err
		}
		interrupted = append(interrupted, slot)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'CANCELLED', assigned_slot = NULL, lease_start = NULL, lease_end = NULL
		WHERE state = 'PREPARING'`)
	if err != nil {
		return 0, err
	}
	reservations, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, updated_at = ? WHERE state = ?`,
		domain.SlotQuarantined, now, domain.SlotPreparing); err != nil {
		return 0, err
	}
	for _, slot := range interrupted {
		key := fmt.Sprintf("RECOVER:%s:%d", slot.id, slot.generation)
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs
			(idempotency_key, slot_id, generation, transition, state, next_attempt_at, created_at, updated_at)
			VALUES(?, ?, ?, 'RECOVER', 'PENDING', ?, ?, ?)
			ON CONFLICT(idempotency_key) DO NOTHING`, key, slot.id, slot.generation, now, now, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return reservations, nil
}

func (s *Store) AssignNext(ctx context.Context, duration time.Duration) (*domain.Assignment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var slotID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM slots WHERE state = ? ORDER BY id LIMIT 1`, domain.SlotAvailable).Scan(&slotID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	assignment := domain.Assignment{SlotID: slotID}
	if err := tx.QueryRowContext(ctx, `
		SELECT id, discord_user_id FROM reservations
		WHERE state = 'QUEUED' ORDER BY requested_at, id LIMIT 1`).Scan(
		&assignment.ReservationID, &assignment.DiscordUserID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	assignment.LeaseStart = time.Now().UTC()
	assignment.LeaseEnd = assignment.LeaseStart.Add(duration)
	start := assignment.LeaseStart.Format(time.RFC3339Nano)
	end := assignment.LeaseEnd.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_owner = ?, lease_start = ?, lease_end = ?,
		generation = generation + 1, updated_at = ? WHERE id = ?`,
		domain.SlotAssigned, assignment.DiscordUserID, start, end, start, slotID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'ASSIGNED', assigned_slot = ?, lease_start = ?, lease_end = ?
		WHERE id = ?`, slotID, start, end, assignment.ReservationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (s *Store) CreateNoShowResetJob(ctx context.Context, slotID string, generation int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ?`, domain.SlotDraining, now,
		slotID, generation, domain.SlotAssigned)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE reservations SET state = 'DRAINING'
		WHERE assigned_slot = ? AND state = 'ASSIGNED'`, slotID); err != nil {
		return false, err
	}
	key := fmt.Sprintf("RESET:%s:%d", slotID, generation)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jobs(idempotency_key, slot_id, generation, transition, state,
		next_attempt_at, created_at, updated_at)
		VALUES(?, ?, ?, 'RESET', 'PENDING', ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`, key, slotID, generation, now, now, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) CreateExpiredLeaseJobs(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, r.assigned_slot, s.generation
		FROM reservations r JOIN slots s ON s.id = r.assigned_slot
		WHERE r.state = 'ASSIGNED' AND r.lease_end <= ? AND s.state = ?
		ORDER BY r.id`, now.UTC().Format(time.RFC3339Nano), domain.SlotAssigned)
	if err != nil {
		return 0, err
	}
	type expired struct {
		reservationID int64
		slotID        string
		generation    int64
	}
	var values []expired
	for rows.Next() {
		var value expired
		if err := rows.Scan(&value.reservationID, &value.slotID, &value.generation); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	created := 0
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, value := range values {
		key := fmt.Sprintf("RESET:%s:%d", value.slotID, value.generation)
		result, err := tx.ExecContext(ctx, `
			INSERT INTO jobs(idempotency_key, slot_id, generation, transition, state,
			next_attempt_at, created_at, updated_at)
			VALUES(?, ?, ?, 'RESET', 'PENDING', ?, ?, ?)
			ON CONFLICT(idempotency_key) DO NOTHING`, key, value.slotID, value.generation, stamp, stamp, stamp)
		if err != nil {
			return 0, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if inserted == 0 {
			continue
		}
		result, err = tx.ExecContext(ctx, `UPDATE reservations SET state = 'DRAINING'
			WHERE id = ? AND state = 'ASSIGNED'`, value.reservationID)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return 0, fmt.Errorf("drain expired reservation %d: concurrent state change", value.reservationID)
		}
		result, err = tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
			WHERE id = ? AND generation = ? AND state = ?`, domain.SlotDraining, stamp,
			value.slotID, value.generation, domain.SlotAssigned)
		if err != nil {
			return 0, err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return 0, fmt.Errorf("drain expired slot %s: concurrent state change", value.slotID)
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}

func (s *Store) ExpireSimulationLeases(ctx context.Context, now time.Time, cooldown time.Duration) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, discord_user_id, assigned_slot FROM reservations
		WHERE state = 'ASSIGNED' AND lease_end <= ? ORDER BY id`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	type expiredLease struct {
		id     int64
		userID string
		slotID string
	}
	var expired []expiredLease
	for rows.Next() {
		var lease expiredLease
		if err := rows.Scan(&lease.id, &lease.userID, &lease.slotID); err != nil {
			rows.Close()
			return 0, err
		}
		expired = append(expired, lease)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, lease := range expired {
		if _, err := tx.ExecContext(ctx, `UPDATE reservations SET state = 'COMPLETED' WHERE id = ?`, lease.id); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE slots SET state = ?, generation = generation + 1, lease_owner = NULL,
			lease_start = NULL, lease_end = NULL, updated_at = ?
			WHERE id = ? AND lease_owner = ?`, domain.SlotAvailable,
			now.UTC().Format(time.RFC3339Nano), lease.slotID, lease.userID)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return 0, fmt.Errorf("expire lease for slot %s: concurrent state change", lease.slotID)
		}
		if cooldown > 0 {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO cooldowns(discord_user_id, next_eligible_at, reason) VALUES(?, ?, 'completed lease')
				ON CONFLICT(discord_user_id) DO UPDATE SET next_eligible_at = excluded.next_eligible_at,
				reason = excluded.reason`, lease.userID, now.UTC().Add(cooldown).Format(time.RFC3339Nano)); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(expired), nil
}

func (s *Store) ExtendLease(ctx context.Context, discordUserID string, extension time.Duration) (time.Time, error) {
	if discordUserID == "" || extension <= 0 || extension > 24*time.Hour {
		return time.Time{}, errors.New("Discord user and extension between zero and 24 hours are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var reservationID int64
	var slotID, currentEnd string
	err = tx.QueryRowContext(ctx, `SELECT id, assigned_slot, lease_end FROM reservations
		WHERE discord_user_id = ? AND state = 'ASSIGNED'`, discordUserID).Scan(&reservationID, &slotID, &currentEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	end, err := time.Parse(time.RFC3339Nano, currentEnd)
	if err != nil {
		return time.Time{}, err
	}
	end = end.Add(extension)
	encoded := end.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE reservations SET lease_end = ? WHERE id = ? AND state = 'ASSIGNED'`, encoded, reservationID); err != nil {
		return time.Time{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE slots SET lease_end = ?, updated_at = ? WHERE id = ? AND state = ? AND lease_owner = ?`,
		encoded, time.Now().UTC().Format(time.RFC3339Nano), slotID, domain.SlotAssigned, discordUserID)
	if err != nil {
		return time.Time{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return time.Time{}, fmt.Errorf("extend lease: concurrent state change")
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return end, nil
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, occurred_at, actor, COALESCE(slot_id, ''), reservation_id,
		action, result, detail FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		var occurred string
		var reservationID sql.NullInt64
		if err := rows.Scan(&event.ID, &occurred, &event.Actor, &event.SlotID, &reservationID,
			&event.Action, &event.Result, &event.Detail); err != nil {
			return nil, err
		}
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, err
		}
		if reservationID.Valid {
			value := reservationID.Int64
			event.ReservationID = &value
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) RetryFailedJob(ctx context.Context, jobID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var slotID string
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT slot_id, generation FROM jobs WHERE id = ? AND state = 'FAILED'`, jobID).Scan(&slotID, &generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'PENDING', attempts = 0,
		next_attempt_at = ?, last_error = NULL, updated_at = ? WHERE id = ? AND state = 'FAILED'`, now, now, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ?`, domain.SlotDraining, now,
		slotID, generation, domain.SlotQuarantined); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RequestRelease(ctx context.Context, discordUserID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var reservationID int64
	var slotID string
	var generation int64
	err = tx.QueryRowContext(ctx, `
		SELECT r.id, r.assigned_slot, s.generation
		FROM reservations r JOIN slots s ON s.id = r.assigned_slot
		WHERE r.discord_user_id = ? AND r.state = 'ASSIGNED' AND s.state = ?`,
		discordUserID, domain.SlotAssigned).Scan(&reservationID, &slotID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE reservations SET state = 'DRAINING'
		WHERE id = ? AND state = 'ASSIGNED'`, reservationID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE slots SET state = ?, updated_at = ?
		WHERE id = ? AND generation = ? AND state = ?`, domain.SlotDraining, now,
		slotID, generation, domain.SlotAssigned)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, fmt.Errorf("request release: concurrent state change")
	}
	key := fmt.Sprintf("RESET:%s:%d", slotID, generation)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jobs(idempotency_key, slot_id, generation, transition, state,
		next_attempt_at, created_at, updated_at)
		VALUES(?, ?, ?, 'RESET', 'PENDING', ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`, key, slotID, generation, now, now, now); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(occurred_at, actor, slot_id, reservation_id, action, result, detail)
		VALUES(?, ?, ?, ?, 'early_release', 'requested', json_object('generation', ?))`,
		now, discordUserID, slotID, reservationID, generation); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) ReleaseAssignment(ctx context.Context, discordUserID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var reservationID int64
	var slotID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, assigned_slot FROM reservations
		WHERE discord_user_id = ? AND state = 'ASSIGNED'`, discordUserID).Scan(&reservationID, &slotID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'COMPLETED' WHERE id = ?`, reservationID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_owner = NULL, lease_start = NULL, lease_end = NULL,
		updated_at = ? WHERE id = ? AND lease_owner = ?`, domain.SlotAvailable,
		time.Now().UTC().Format(time.RFC3339Nano), slotID, discordUserID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RejectAssignment(ctx context.Context, assignment domain.Assignment, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'CANCELLED', assigned_slot = NULL, lease_start = NULL, lease_end = NULL
		WHERE id = ? AND state = 'ASSIGNED' AND discord_user_id = ?`,
		assignment.ReservationID, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("reject reservation %d: concurrent state change", assignment.ReservationID)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_owner = NULL, lease_start = NULL, lease_end = NULL,
		updated_at = ? WHERE id = ? AND lease_owner = ?`,
		domain.SlotAvailable, time.Now().UTC().Format(time.RFC3339Nano), assignment.SlotID, assignment.DiscordUserID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("release rejected assignment slot %s: concurrent state change", assignment.SlotID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events(occurred_at, actor, slot_id, reservation_id, action, result, detail)
		VALUES(?, ?, ?, ?, 'assignment_eligibility', 'denied', json_object('reason', ?))`,
		time.Now().UTC().Format(time.RFC3339Nano), assignment.DiscordUserID,
		assignment.SlotID, assignment.ReservationID, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetryAssignment(ctx context.Context, assignment domain.Assignment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE reservations SET state = 'QUEUED', assigned_slot = NULL, lease_start = NULL, lease_end = NULL
		WHERE id = ? AND state = 'ASSIGNED'`, assignment.ReservationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE slots SET state = ?, lease_owner = NULL, lease_start = NULL, lease_end = NULL,
		updated_at = ? WHERE id = ? AND lease_owner = ?`,
		domain.SlotAvailable, time.Now().UTC().Format(time.RFC3339Nano), assignment.SlotID, assignment.DiscordUserID); err != nil {
		return err
	}
	return tx.Commit()
}
