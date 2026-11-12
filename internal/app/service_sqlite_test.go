package app_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/app"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

func TestConcurrentDuplicateJoinHasOneReservation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := app.New(database, false, 4*time.Hour)
	ctx := context.Background()

	const workers = 20
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := service.Join(ctx, "same-participant")
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	reservations, err := database.ListReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 1 || reservations[0].DiscordUserID != "same-participant" {
		t.Fatalf("reservations=%+v", reservations)
	}
}
