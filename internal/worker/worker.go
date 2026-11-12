package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type Worker struct {
	store       *store.Store
	log         *slog.Logger
	cooldown    time.Duration
	retryBase   time.Duration
	noShowGrace time.Duration
}

func New(database *store.Store, log *slog.Logger, cooldown time.Duration, noShowGrace ...time.Duration) *Worker {
	grace := 10 * time.Minute
	if len(noShowGrace) > 0 && noShowGrace[0] > 0 {
		grace = noShowGrace[0]
	}
	return &Worker{store: database, log: log, cooldown: cooldown, retryBase: 10 * time.Second, noShowGrace: grace}
}

func (w *Worker) RunSimulation(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		w.expireLeases(ctx)
		w.runOne(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunProduction(ctx context.Context, client ludus.Client) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		w.reclaimNoShows(ctx, client, time.Now().UTC())
		created, err := w.store.CreateExpiredLeaseJobs(ctx, time.Now().UTC())
		if err != nil && !errors.Is(err, context.Canceled) {
			w.log.Error("create expired production lease jobs", "error", err)
		}
		if created > 0 {
			w.log.Info("created expired production lease jobs", "count", created)
		}
		for {
			err := w.RunProductionOnce(ctx, client)
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, context.Canceled) {
				break
			}
			if err != nil {
				w.log.Error("production lifecycle job iteration failed", "error", err)
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunProductionOnce(ctx context.Context, client ludus.Client) error {
	job, err := w.store.ClaimNextJob(ctx)
	if err != nil {
		return err
	}
	w.log.Info("executing production lifecycle job", "job_id", job.ID, "transition", job.Transition, "slot", job.SlotID, "attempt", job.Attempts)
	executionErr := w.executeProduction(ctx, client, job.Transition, job.SlotID, job.Generation)
	if executionErr != nil {
		failure := executionErr.Error()
		if job.Attempts < 3 {
			delay := time.Duration(job.Attempts*job.Attempts) * w.retryBase
			if err := w.store.RetryJob(ctx, job.ID, failure, delay); err != nil {
				return fmt.Errorf("record production retry after %v: %w", executionErr, err)
			}
			return fmt.Errorf("production job %d scheduled for retry: %w", job.ID, executionErr)
		}
		if err := w.store.FailJobAndQuarantine(ctx, *job, failure); err != nil {
			return fmt.Errorf("quarantine after production failure %v: %w", executionErr, err)
		}
		return fmt.Errorf("production job %d failed and slot %s was quarantined: %w", job.ID, job.SlotID, executionErr)
	}
	if err := w.store.CompleteProductionJob(ctx, *job, w.cooldown); err != nil {
		return fmt.Errorf("complete production job %d: %w", job.ID, err)
	}
	w.log.Info("production lifecycle job succeeded", "job_id", job.ID, "transition", job.Transition, "slot", job.SlotID)
	return nil
}

func (w *Worker) executeProduction(ctx context.Context, client ludus.Client, transition, slotID string, generation int64) error {
	switch transition {
	case "RESET":
		if err := w.revokeWireGuard(ctx, client, slotID, generation); err != nil {
			return err
		}
		if err := client.ResetRange(ctx, slotID); err != nil {
			return err
		}
		readiness, err := client.VerifyRange(ctx, slotID)
		if err != nil {
			return err
		}
		if !readiness.Ready {
			return fmt.Errorf("post-reset readiness failed: %v", readiness.Failures)
		}
		return nil
	case "QUARANTINE":
		return nil
	case "RECOVER":
		if _, err := client.ValidateBlueprint(ctx, slotID); err != nil {
			return err
		}
		if err := w.revokeWireGuard(ctx, client, slotID, generation); err != nil {
			return fmt.Errorf("revoke WireGuard during recovery: %w", err)
		}
		if err := client.ResetRange(ctx, slotID); err != nil {
			return fmt.Errorf("reset range during recovery: %w", err)
		}
		inventory, err := client.Inventory(ctx, slotID)
		if err != nil {
			return err
		}
		if !inventory.Testing {
			return errors.New("cannot recover slot without active testing baseline")
		}
		readiness, err := client.VerifyRange(ctx, slotID)
		if err != nil {
			return err
		}
		if !readiness.Ready {
			return fmt.Errorf("recovery readiness failed: %v", readiness.Failures)
		}
		return nil
	default:
		return fmt.Errorf("unsupported production transition %q", transition)
	}
}

func (w *Worker) reclaimNoShows(ctx context.Context, client ludus.Client, now time.Time) {
	candidates, err := w.store.NoShowCandidates(ctx, now.Add(-w.noShowGrace))
	if err != nil {
		w.log.Error("list WireGuard no-show candidates", "error", err)
		return
	}
	for _, slot := range candidates {
		peer, err := client.WireGuardStatus(ctx, slot.ID)
		if err != nil {
			w.log.Error("check WireGuard no-show status", "slot", slot.ID, "error", err)
			continue
		}
		if peer.Generation != slot.Generation || peer.PeerID != slot.WireGuardPeerID {
			w.log.Error("WireGuard no-show status generation mismatch", "slot", slot.ID, "generation", slot.Generation)
			continue
		}
		if peer.LatestHandshake != nil && slot.WireGuardIssuedAt != nil && !peer.LatestHandshake.Before(*slot.WireGuardIssuedAt) {
			if err := w.store.RecordWireGuardHandshake(ctx, slot.ID, slot.Generation, *peer.LatestHandshake); err != nil {
				w.log.Error("record first WireGuard handshake", "slot", slot.ID, "error", err)
			}
			continue
		}
		created, err := w.store.CreateNoShowResetJob(ctx, slot.ID, slot.Generation)
		if err != nil {
			w.log.Error("enqueue WireGuard no-show reset", "slot", slot.ID, "error", err)
			continue
		}
		if created {
			w.log.Info("reclaiming lease without WireGuard handshake", "slot", slot.ID, "generation", slot.Generation)
		}
	}
}

func (w *Worker) revokeWireGuard(ctx context.Context, client ludus.Client, slotID string, generation int64) error {
	status, statusErr := client.WireGuardStatus(ctx, slotID)
	if statusErr == nil && status.Generation > generation {
		return fmt.Errorf("refusing stale lifecycle job generation %d because plugin is at generation %d", generation, status.Generation)
	}
	peer := status
	if statusErr != nil || !status.Revoked {
		revokeGeneration := generation
		if statusErr == nil {
			revokeGeneration = status.Generation
		}
		var err error
		peer, err = client.RevokeWireGuard(ctx, slotID, revokeGeneration)
		if err != nil {
			return fmt.Errorf("revoke WireGuard peer: %w", err)
		}
	}
	if !peer.Revoked {
		return errors.New("WireGuard peer revocation was not confirmed")
	}
	revokedAt := peer.RevokedAt
	if revokedAt == nil {
		return errors.New("WireGuard peer revocation omitted its timestamp")
	}
	if err := w.store.RecordWireGuardRevoked(ctx, slotID, generation, *revokedAt); err != nil {
		return fmt.Errorf("record WireGuard revocation: %w", err)
	}
	return nil
}

func (w *Worker) expireLeases(ctx context.Context) {
	expired, err := w.store.ExpireSimulationLeases(ctx, time.Now().UTC(), w.cooldown)
	if err != nil && !errors.Is(err, context.Canceled) {
		w.log.Error("expire simulated leases", "error", err)
		return
	}
	if expired > 0 {
		w.log.Info("expired simulated leases", "count", expired)
	}
}

func (w *Worker) runOne(ctx context.Context) {
	job, err := w.store.ClaimNextJob(ctx)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		w.log.Error("claim lifecycle job", "error", err)
		return
	}
	w.log.Info("executing simulated lifecycle job", "job_id", job.ID, "transition", job.Transition, "slot", job.SlotID)
	if err := w.store.CompleteSimulationJob(ctx, *job); err != nil {
		w.log.Error("simulated lifecycle job failed", "job_id", job.ID, "error", err)
		if failErr := w.store.FailJob(ctx, job.ID, err.Error()); failErr != nil {
			w.log.Error("record lifecycle job failure", "job_id", job.ID, "error", failErr)
		}
		return
	}
	w.log.Info("simulated lifecycle job succeeded", "job_id", job.ID, "transition", job.Transition, "slot", job.SlotID)
}
