package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type fakeReservations struct {
	mu           sync.Mutex
	reservations map[string]domain.Reservation
	cooldowns    map[string]time.Time
	nextID       int64
}

func newFakeReservations() *fakeReservations {
	return &fakeReservations{reservations: make(map[string]domain.Reservation), cooldowns: make(map[string]time.Time), nextID: 1}
}

func (f *fakeReservations) ActiveReservation(_ context.Context, user string) (domain.Reservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation, ok := f.reservations[user]
	if !ok {
		return domain.Reservation{}, store.ErrNotFound
	}
	return reservation, nil
}

func (f *fakeReservations) CooldownUntil(_ context.Context, user string) (*time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.cooldowns[user]
	if !exists {
		return nil, nil
	}
	return &value, nil
}

func (f *fakeReservations) SetCooldown(_ context.Context, user, _ string, until time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cooldowns[user] = until
	return nil
}

func (f *fakeReservations) Enqueue(_ context.Context, user string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.reservations[user]; exists {
		return 0, errors.New("duplicate")
	}
	id := f.nextID
	f.nextID++
	f.reservations[user] = domain.Reservation{ID: id, DiscordUserID: user, State: "QUEUED", QueuePosition: len(f.reservations) + 1}
	return id, nil
}

func (f *fakeReservations) CancelQueued(_ context.Context, user string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation, exists := f.reservations[user]
	if !exists || reservation.State != "QUEUED" {
		return false, nil
	}
	delete(f.reservations, user)
	return true, nil
}

func (f *fakeReservations) RequestRelease(_ context.Context, user string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation, exists := f.reservations[user]
	if !exists || reservation.State != "ASSIGNED" {
		return false, nil
	}
	reservation.State = "DRAINING"
	f.reservations[user] = reservation
	return true, nil
}

func (f *fakeReservations) ReleaseAssignment(_ context.Context, user string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation, exists := f.reservations[user]
	if !exists || reservation.State != "ASSIGNED" {
		return false, nil
	}
	delete(f.reservations, user)
	return true, nil
}

func TestJoinIsIdempotent(t *testing.T) {
	database := newFakeReservations()
	service := New(database, false, 4*time.Hour)
	first, created, err := service.Join(context.Background(), "participant")
	if err != nil || !created || first.State != "QUEUED" {
		t.Fatalf("first join = %+v, %v, %v", first, created, err)
	}
	second, created, err := service.Join(context.Background(), "participant")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second join = %+v, %v, %v", second, created, err)
	}
}

func TestLeavePolicy(t *testing.T) {
	database := newFakeReservations()
	database.reservations["queued"] = domain.Reservation{DiscordUserID: "queued", State: "QUEUED"}
	database.reservations["assigned"] = domain.Reservation{DiscordUserID: "assigned", State: "ASSIGNED"}

	production := New(database, false, 4*time.Hour)
	state, err := production.Leave(context.Background(), "queued")
	if err != nil || state != "CANCELLED" {
		t.Fatalf("queued leave = %q, %v", state, err)
	}
	state, err = production.Leave(context.Background(), "assigned")
	if err != nil || state != "RELEASING" {
		t.Fatalf("assigned production leave = %q, %v", state, err)
	}

	database.reservations["assigned"] = domain.Reservation{DiscordUserID: "assigned", State: "ASSIGNED"}
	simulation := New(database, true, 4*time.Hour)
	state, err = simulation.Leave(context.Background(), "assigned")
	if err != nil || state != "RELEASED" {
		t.Fatalf("assigned simulation leave = %q, %v", state, err)
	}
	if _, _, err := simulation.Join(context.Background(), "assigned"); err == nil {
		t.Fatal("participant rejoined during cooldown")
	} else {
		var cooldown CooldownError
		if !errors.As(err, &cooldown) {
			t.Fatalf("expected CooldownError, got %v", err)
		}
	}
}
