package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestExpireSimulationLeaseSetsCooldownAndReleasesSlot(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "broker.db"))
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
	if _, err := database.AssignNext(ctx, -time.Minute); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expired, err := database.ExpireSimulationLeases(ctx, now, 4*time.Hour)
	if err != nil || expired != 1 {
		t.Fatalf("expired=%d err=%v", expired, err)
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != "AVAILABLE" || slots[0].Generation != 2 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	until, err := database.CooldownUntil(ctx, "participant")
	if err != nil || until == nil || !until.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("cooldown=%v err=%v", until, err)
	}
}
