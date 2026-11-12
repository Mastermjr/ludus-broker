package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type CooldownError struct {
	Until time.Time
}

func (e CooldownError) Error() string {
	return "participant is in cooldown until " + e.Until.UTC().Format(time.RFC3339)
}

type ReservationStore interface {
	ActiveReservation(context.Context, string) (domain.Reservation, error)
	Enqueue(context.Context, string) (int64, error)
	CancelQueued(context.Context, string) (bool, error)
	RequestRelease(context.Context, string) (bool, error)
	ReleaseAssignment(context.Context, string) (bool, error)
	CooldownUntil(context.Context, string) (*time.Time, error)
	SetCooldown(context.Context, string, string, time.Time) error
}

type Service struct {
	store            ReservationStore
	simulationMode   bool
	cooldownDuration time.Duration
	now              func() time.Time
}

func New(database ReservationStore, simulationMode bool, cooldownDuration time.Duration) *Service {
	return &Service{store: database, simulationMode: simulationMode, cooldownDuration: cooldownDuration, now: time.Now}
}

func (s *Service) Join(ctx context.Context, discordUserID string) (domain.Reservation, bool, error) {
	if discordUserID == "" {
		return domain.Reservation{}, false, errors.New("Discord user ID is required")
	}
	reservation, err := s.store.ActiveReservation(ctx, discordUserID)
	if err == nil {
		return reservation, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.Reservation{}, false, fmt.Errorf("look up reservation: %w", err)
	}
	until, err := s.store.CooldownUntil(ctx, discordUserID)
	if err != nil {
		return domain.Reservation{}, false, fmt.Errorf("look up cooldown: %w", err)
	}
	if until != nil && until.After(s.now().UTC()) {
		return domain.Reservation{}, false, CooldownError{Until: *until}
	}
	if _, err := s.store.Enqueue(ctx, discordUserID); err != nil {
		// A concurrent duplicate join can win after the first lookup. Return the
		// resulting active reservation instead of surfacing a uniqueness error.
		reservation, lookupErr := s.store.ActiveReservation(ctx, discordUserID)
		if lookupErr == nil {
			return reservation, false, nil
		}
		return domain.Reservation{}, false, fmt.Errorf("enqueue participant: %w", err)
	}
	reservation, err = s.store.ActiveReservation(ctx, discordUserID)
	if err != nil {
		return domain.Reservation{}, false, fmt.Errorf("load queued reservation: %w", err)
	}
	return reservation, true, nil
}

func (s *Service) Leave(ctx context.Context, discordUserID string) (string, error) {
	if discordUserID == "" {
		return "", errors.New("Discord user ID is required")
	}
	reservation, err := s.store.ActiveReservation(ctx, discordUserID)
	if errors.Is(err, store.ErrNotFound) {
		return "NONE", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up reservation: %w", err)
	}
	if reservation.State == "ASSIGNED" {
		if !s.simulationMode {
			released, err := s.store.RequestRelease(ctx, discordUserID)
			if err != nil {
				return "", fmt.Errorf("request production lease release: %w", err)
			}
			if released {
				return "RELEASING", nil
			}
			return "NONE", nil
		}
		released, err := s.store.ReleaseAssignment(ctx, discordUserID)
		if err != nil {
			return "", fmt.Errorf("release simulated lease: %w", err)
		}
		if released {
			if s.cooldownDuration > 0 {
				until := s.now().UTC().Add(s.cooldownDuration)
				if err := s.store.SetCooldown(ctx, discordUserID, "completed lease", until); err != nil {
					return "", fmt.Errorf("set participant cooldown: %w", err)
				}
			}
			return "RELEASED", nil
		}
		return "NONE", nil
	}
	cancelled, err := s.store.CancelQueued(ctx, discordUserID)
	if err != nil {
		return "", fmt.Errorf("cancel queue entry: %w", err)
	}
	if !cancelled {
		return "NONE", nil
	}
	return "CANCELLED", nil
}

func (s *Service) Status(ctx context.Context, discordUserID string) (domain.Reservation, error) {
	if discordUserID == "" {
		return domain.Reservation{}, errors.New("Discord user ID is required")
	}
	reservation, err := s.store.ActiveReservation(ctx, discordUserID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Reservation{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("look up reservation: %w", err)
	}
	return reservation, nil
}
