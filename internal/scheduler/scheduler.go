package scheduler

import (
	"context"
	"log/slog"
	"time"
)

type Reconciler interface {
	Reconcile(context.Context, time.Time) error
}

type Scheduler struct {
	interval   time.Duration
	reconciler Reconciler
	log        *slog.Logger
}

func New(interval time.Duration, reconciler Reconciler, log *slog.Logger) *Scheduler {
	return &Scheduler{interval: interval, reconciler: reconciler, log: log}
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if err := s.reconciler.Reconcile(ctx, time.Now().UTC()); err != nil {
			s.log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
