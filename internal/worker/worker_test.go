package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type fakeLudus struct {
	resetErr    error
	resets      int
	revocations int
	status      ludus.WireGuardPeer
}

func (f *fakeLudus) Version(context.Context) (ludus.Version, error) {
	return ludus.Version{Server: "test", API: "v2"}, nil
}
func (f *fakeLudus) Inventory(context.Context, string) (ludus.Range, error) {
	return ludus.Range{ID: "user1", State: "SUCCESS", Testing: true, VMs: []ludus.VM{{PoweredOn: true}, {PoweredOn: true}, {PoweredOn: true}, {PoweredOn: true}}}, nil
}
func (f *fakeLudus) ValidateBlueprint(context.Context, string) (string, error) {
	return "canonical", nil
}
func (f *fakeLudus) WireGuardHealth(context.Context) (ludus.WireGuardCapability, error) {
	return ludus.WireGuardCapability{Name: "viberange-wireguard", Version: "test", Ready: true}, nil
}
func (f *fakeLudus) IssueWireGuard(context.Context, string, int64) (*ludus.WireGuardAccess, error) {
	return ludus.NewWireGuardAccess(ludus.WireGuardPeer{UserID: "user1", Generation: 1, PeerID: "peer", IssuedAt: time.Now().UTC()}, []byte("[Interface]\nPrivateKey = test")), nil
}
func (f *fakeLudus) RevokeWireGuard(_ context.Context, userID string, generation int64) (ludus.WireGuardPeer, error) {
	f.revocations++
	now := time.Now().UTC()
	return ludus.WireGuardPeer{UserID: userID, Generation: generation, PeerID: "peer", Revoked: true, RevokedAt: &now}, nil
}
func (f *fakeLudus) WireGuardStatus(_ context.Context, _ string) (ludus.WireGuardPeer, error) {
	if f.status.UserID != "" {
		return f.status, nil
	}
	return ludus.WireGuardPeer{}, store.ErrNotFound
}
func (f *fakeLudus) ResetRange(context.Context, string) error {
	f.resets++
	return f.resetErr
}
func (f *fakeLudus) VerifyRange(context.Context, string) (ludus.Readiness, error) {
	return ludus.Readiness{Ready: true}, nil
}

func TestProductionResetJob(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateSlotJob(ctx, "RESET", "user1"); err != nil {
		t.Fatal(err)
	}
	client := &fakeLudus{}
	value := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err := value.RunProductionOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	if client.resets != 1 || client.revocations != 1 {
		t.Fatalf("reset calls = %d revocations = %d, want 1 each", client.resets, client.revocations)
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotAvailable || slots[0].Generation != 1 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
}

func TestProductionExpiredLeaseRevokesResetsAndCoolsDown(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	assignment, err := database.PrepareNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assignment.LeaseStart = time.Now().UTC().Add(-2 * time.Hour)
	assignment.LeaseEnd = time.Now().UTC().Add(-time.Minute)
	if err := database.RecordWireGuardIssued(ctx, *assignment, "sha256:peer", assignment.LeaseStart); err != nil {
		t.Fatal(err)
	}
	if err := database.CompletePreparation(ctx, *assignment); err != nil {
		t.Fatal(err)
	}
	created, err := database.CreateExpiredLeaseJobs(ctx, time.Now().UTC())
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	client := &fakeLudus{}
	value := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), 4*time.Hour)
	if err := value.RunProductionOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	if client.revocations != 1 || client.resets != 1 {
		t.Fatalf("revocations=%d resets=%d", client.revocations, client.resets)
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotAvailable {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	until, err := database.CooldownUntil(ctx, "discord-user")
	if err != nil || until == nil || !until.After(time.Now().UTC().Add(3*time.Hour)) {
		t.Fatalf("cooldown=%v err=%v", until, err)
	}
}

func TestNoShowReclamationUsesWireGuardHandshake(t *testing.T) {
	for _, test := range []struct {
		name          string
		handshake     bool
		wantDraining  bool
		wantHandshake bool
	}{
		{name: "no handshake enqueues reset", wantDraining: true},
		{name: "handshake retains lease", handshake: true, wantHandshake: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx := context.Background()
			if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
				t.Fatal(err)
			}
			assignment, err := database.PrepareNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			issuedAt := time.Now().UTC().Add(-20 * time.Minute)
			if err := database.RecordWireGuardIssued(ctx, *assignment, "peer", issuedAt); err != nil {
				t.Fatal(err)
			}
			assignment.LeaseStart = issuedAt
			assignment.LeaseEnd = issuedAt.Add(2 * time.Hour)
			if err := database.CompletePreparation(ctx, *assignment); err != nil {
				t.Fatal(err)
			}
			peer := ludus.WireGuardPeer{UserID: "user1", Generation: assignment.Generation, PeerID: "peer"}
			if test.handshake {
				handshake := issuedAt.Add(time.Minute)
				peer.LatestHandshake = &handshake
			}
			value := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), 0, 10*time.Minute)
			value.reclaimNoShows(ctx, &fakeLudus{status: peer}, time.Now().UTC())
			slots, err := database.ListSlots(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := slots[0].State == domain.SlotDraining; got != test.wantDraining {
				t.Fatalf("slot=%+v wantDraining=%v", slots[0], test.wantDraining)
			}
			if got := slots[0].WireGuardFirstHandshake != nil; got != test.wantHandshake {
				t.Fatalf("slot=%+v wantHandshake=%v", slots[0], test.wantHandshake)
			}
		})
	}
}

func TestRecoverInterruptedPreparationWithPreviouslyRevokedPeer(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "participant"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PrepareNext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RecoverInterruptedPreparations(ctx); err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Now().UTC().Add(-time.Minute)
	client := &fakeLudus{status: ludus.WireGuardPeer{
		UserID: "user1", Generation: 0, PeerID: "previous", Revoked: true, RevokedAt: &revokedAt,
	}}
	value := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err := value.RunProductionOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	if client.revocations != 0 || client.resets != 1 {
		t.Fatalf("revocations=%d resets=%d", client.revocations, client.resets)
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotAvailable || slots[0].Generation != 2 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
}

func TestProductionFailureRetriesThenQuarantines(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateSlotJob(ctx, "RESET", "user1"); err != nil {
		t.Fatal(err)
	}
	client := &fakeLudus{resetErr: errors.New("external failure")}
	value := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	value.retryBase = 0
	for attempt := 1; attempt <= 3; attempt++ {
		err := value.RunProductionOnce(ctx, client)
		if err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotQuarantined {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	jobs, err := database.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].State != "FAILED" || jobs[0].Attempts != 3 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestSimulationLifecycleJobs(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := New(database, log, 4*time.Hour)

	if _, _, err := database.CreateSlotJob(ctx, "QUARANTINE", "user1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimNextJob(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.RecoverInterruptedJobs(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	worker.runOne(ctx)
	slots, err := database.ListSlots(ctx)
	if err != nil || len(slots) != 1 || slots[0].State != domain.SlotQuarantined {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	if _, _, err := database.CreateSlotJob(ctx, "RECOVER", "user1"); err != nil {
		t.Fatal(err)
	}
	worker.runOne(ctx)
	slots, err = database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotAvailable || slots[0].Generation != 1 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	jobs, err := database.ListJobs(ctx)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	for _, job := range jobs {
		if job.State != "SUCCEEDED" {
			t.Fatalf("job did not succeed: %+v", job)
		}
	}
}
