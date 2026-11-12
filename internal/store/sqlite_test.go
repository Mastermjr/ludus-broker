package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

func TestSchemaCannotPersistWireGuardProfiles(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(slots)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "wireguard_profile" || name == "wireguard_private_key" {
			t.Fatalf("secret-bearing schema column exists: %s", name)
		}
	}
}

func TestCreateWireGuardDriftRecoveryJob(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user2"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO reservations
		(discord_user_id, state, requested_at, assigned_slot) VALUES('participant', 'DRAINING', ?, 'user2')`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE slots SET state = 'QUARANTINED', generation = 3,
		lease_owner = 'participant' WHERE id = 'user2'`); err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Minute)
	revoked := issued.Add(time.Second)
	jobID, err := store.CreateWireGuardDriftRecoveryJob(ctx, "user2", 6, "sha256:newer", issued, revoked)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	var state, peerID, jobState, transition string
	if err := store.db.QueryRow(`SELECT generation, state, wireguard_peer_id FROM slots WHERE id = 'user2'`).Scan(&generation, &state, &peerID); err != nil {
		t.Fatal(err)
	}
	if generation != 6 || state != "QUARANTINED" || peerID != "sha256:newer" {
		t.Fatalf("generation=%d state=%s peer=%s", generation, state, peerID)
	}
	if err := store.db.QueryRow(`SELECT state, transition FROM jobs WHERE id = ?`, jobID).Scan(&jobState, &transition); err != nil {
		t.Fatal(err)
	}
	if jobState != "PENDING" || transition != "RESET" {
		t.Fatalf("job state=%s transition=%s", jobState, transition)
	}
	if _, err := store.CreateWireGuardDriftRecoveryJob(ctx, "user2", 7, "sha256:other", issued, revoked); err == nil {
		t.Fatal("second recovery was accepted while an active job exists")
	}
}

func TestProductionPreparationLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.PrepareNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.SlotID != "user1" || assignment.Generation != 1 {
		t.Fatalf("assignment = %+v", assignment)
	}
	reservation, err := store.ActiveReservation(ctx, "discord-user")
	if err != nil || reservation.State != "PREPARING" {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if _, err := store.Enqueue(ctx, "discord-user"); err == nil {
		t.Fatal("duplicate reservation was accepted while preparing")
	}
	assignment.LeaseStart = time.Now().UTC()
	assignment.LeaseEnd = assignment.LeaseStart.Add(2 * time.Hour)
	if err := store.RecordWireGuardIssued(ctx, *assignment, "sha256:peer", assignment.LeaseStart); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePreparation(ctx, *assignment); err != nil {
		t.Fatal(err)
	}
	reservation, err = store.ActiveReservation(ctx, "discord-user")
	if err != nil || reservation.State != "ASSIGNED" || reservation.LeaseEnd == nil {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
}

func TestInterruptedPreparationFailsClosed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareNext(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverInterruptedPreparations(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	if _, err := store.ActiveReservation(ctx, "discord-user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ActiveReservation() error = %v, want ErrNotFound", err)
	}
	slots, err := store.ListSlots(ctx)
	if err != nil || slots[0].State != "QUARANTINED" {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Transition != "RECOVER" || jobs[0].State != "PENDING" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestListAndClearCooldownIsScopedAndAudited(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SetCooldown(ctx, "participant-a", "completed lease", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCooldown(ctx, "participant-b", "completed lease", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	values, err := store.ListActiveCooldowns(ctx, now)
	if err != nil || len(values) != 2 {
		t.Fatalf("cooldowns=%+v err=%v", values, err)
	}
	if err := store.ClearCooldown(ctx, "participant-a"); err != nil {
		t.Fatal(err)
	}
	if until, err := store.CooldownUntil(ctx, "participant-a"); err != nil || until != nil {
		t.Fatalf("cleared cooldown=%v err=%v", until, err)
	}
	if until, err := store.CooldownUntil(ctx, "participant-b"); err != nil || until == nil {
		t.Fatalf("unrelated cooldown=%v err=%v", until, err)
	}
	if err := store.ClearCooldown(ctx, "participant-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second clear error=%v", err)
	}
	events, err := store.ListAuditEvents(ctx, 10)
	if err != nil || len(events) != 1 || events[0].Action != "cooldown_clear" || !strings.Contains(events[0].Detail, "participant-a") {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestReconcileAvailableGeneration(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileAvailableGeneration(ctx, "user1", 3); err != nil {
		t.Fatal(err)
	}
	slots, err := store.ListSlots(ctx)
	if err != nil || slots[0].Generation != 3 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	if err := store.ReconcileAvailableGeneration(ctx, "user1", 2); err == nil {
		t.Fatal("generation decrease was accepted")
	}
	if _, err := store.Enqueue(ctx, "participant"); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.PrepareNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileAvailableGeneration(ctx, "user1", assignment.Generation+1); err == nil {
		t.Fatal("reconciled a non-available slot")
	}
}

func TestPreferredAssignmentAndLeaseExtension(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1", "user2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueuePreferred(ctx, "participant", "user2"); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.PrepareNext(ctx)
	if err != nil || assignment.SlotID != "user2" {
		t.Fatalf("assignment=%+v err=%v", assignment, err)
	}
	issued := time.Now().UTC()
	if err := store.RecordWireGuardIssued(ctx, *assignment, "peer", issued); err != nil {
		t.Fatal(err)
	}
	assignment.LeaseStart = issued
	assignment.LeaseEnd = issued.Add(time.Hour)
	if err := store.CompletePreparation(ctx, *assignment); err != nil {
		t.Fatal(err)
	}
	end, err := store.ExtendLease(ctx, "participant", 30*time.Minute)
	if err != nil || end.Sub(assignment.LeaseEnd) != 30*time.Minute {
		t.Fatalf("end=%v err=%v", end, err)
	}
}

func TestRetryFailedJobRestoresPendingDrain(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateSlotJob(ctx, "RESET", "user1"); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimNextJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailJobAndQuarantine(ctx, *job, "forced"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryFailedJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || jobs[0].State != "PENDING" || jobs[0].Attempts != 0 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	slots, err := store.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotDraining {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
}

func TestProductionReleaseCreatesDurableReset(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "participant"); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.PrepareNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC()
	if err := store.RecordWireGuardIssued(ctx, *assignment, "peer", issued); err != nil {
		t.Fatal(err)
	}
	assignment.LeaseStart = issued
	assignment.LeaseEnd = issued.Add(2 * time.Hour)
	if err := store.CompletePreparation(ctx, *assignment); err != nil {
		t.Fatal(err)
	}
	released, err := store.RequestRelease(ctx, "participant")
	if err != nil || !released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	reservation, err := store.ActiveReservation(ctx, "participant")
	if err != nil || reservation.State != "DRAINING" {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Transition != "RESET" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestRejectAssignmentCancelsReservationAndReleasesSlot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.AssignNext(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RejectAssignment(ctx, *assignment, "role removed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveReservation(ctx, "discord-user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ActiveReservation() error = %v, want ErrNotFound", err)
	}
	slots, err := store.ListSlots(ctx)
	if err != nil || len(slots) != 1 || slots[0].State != "AVAILABLE" || slots[0].LeaseOwner != "" {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	var auditCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action = 'assignment_eligibility' AND result = 'denied'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1", auditCount)
	}
}

func TestSeedAndQueue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SeedSlots(ctx, []string{"user1", "user2"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSlots(ctx, []string{"user1", "user2"}); err != nil {
		t.Fatal(err)
	}
	count, err := store.SlotCount(ctx)
	if err != nil || count != 2 {
		t.Fatalf("slot count=%d err=%v", count, err)
	}
	if _, err := store.Enqueue(ctx, "123456789012345678"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "123456789012345678"); err == nil {
		t.Fatal("duplicate active reservation should fail")
	}
	reservation, err := store.ActiveReservation(ctx, "123456789012345678")
	if err != nil || reservation.QueuePosition != 1 {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	assignment, err := store.AssignNext(ctx, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.SlotID != "user1" || assignment.DiscordUserID != "123456789012345678" {
		t.Fatalf("unexpected assignment: %+v", assignment)
	}
	reservation, err = store.ActiveReservation(ctx, "123456789012345678")
	if err != nil || reservation.State != "ASSIGNED" || reservation.AssignedSlot != "user1" {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	released, err := store.ReleaseAssignment(ctx, assignment.DiscordUserID)
	if err != nil || !released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	if _, err := store.Enqueue(ctx, assignment.DiscordUserID); err != nil {
		t.Fatal(err)
	}
	assignment, err = store.AssignNext(ctx, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetryAssignment(ctx, *assignment); err != nil {
		t.Fatal(err)
	}
	reservation, err = store.ActiveReservation(ctx, "123456789012345678")
	if err != nil || reservation.State != "QUEUED" {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	cancelled, err := store.CancelQueued(ctx, "123456789012345678")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	_, err = store.ActiveReservation(ctx, "123456789012345678")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	slots, err := store.ListSlots(ctx)
	if err != nil || len(slots) != 2 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	jobID, created, err := store.CreateSlotJob(ctx, "RESET", "user1")
	if err != nil || !created || jobID == 0 {
		t.Fatalf("jobID=%d created=%v err=%v", jobID, created, err)
	}
	duplicateID, created, err := store.CreateSlotJob(ctx, "RESET", "user1")
	if err != nil || created || duplicateID != jobID {
		t.Fatalf("duplicateID=%d created=%v err=%v", duplicateID, created, err)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Transition != "RESET" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}
