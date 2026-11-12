package main

import (
	"fmt"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

type cooldownView struct {
	DisplayName   string    `json:"display_name"`
	DiscordUserID string    `json:"discord_user_id"`
	Until         time.Time `json:"until"`
	Reason        string    `json:"reason"`
}

type participantHealth struct {
	DisplayName      string     `json:"display_name"`
	DiscordUserID    string     `json:"discord_user_id"`
	ReservationState string     `json:"reservation_state"`
	SlotID           string     `json:"slot_id,omitempty"`
	SlotState        string     `json:"slot_state,omitempty"`
	Health           string     `json:"health"`
	Detail           string     `json:"detail"`
	QueuePosition    int        `json:"queue_position,omitempty"`
	LeaseEnd         *time.Time `json:"lease_end,omitempty"`
}

func participantHealthList(reservations []domain.Reservation, slots []domain.Slot, jobs []domain.Job, names map[string]string, now time.Time, preparationLimit, noShowGrace time.Duration) []participantHealth {
	slotsByID := make(map[string]domain.Slot, len(slots))
	for _, slot := range slots {
		slotsByID[slot.ID] = slot
	}
	latestJob := make(map[string]domain.Job)
	for _, job := range jobs {
		if current, ok := latestJob[job.SlotID]; !ok || job.ID > current.ID {
			latestJob[job.SlotID] = job
		}
	}
	result := make([]participantHealth, 0, len(reservations))
	for _, reservation := range reservations {
		value := participantHealth{
			DisplayName: names[reservation.DiscordUserID], DiscordUserID: reservation.DiscordUserID,
			ReservationState: reservation.State, SlotID: reservation.AssignedSlot,
			QueuePosition: reservation.QueuePosition, LeaseEnd: reservation.LeaseEnd,
		}
		if value.DisplayName == "" {
			value.DisplayName = "<unresolved>"
		}
		slot, hasSlot := slotsByID[reservation.AssignedSlot]
		if hasSlot {
			value.SlotState = string(slot.State)
		}
		switch reservation.State {
		case "QUEUED":
			value.Health = "WAITING"
			value.Detail = fmt.Sprintf("queue position %d", reservation.QueuePosition)
		case "PREPARING":
			value.Health = "PREPARING"
			value.Detail = "clean baseline and WireGuard access are being prepared"
			started := reservation.RequestedAt
			if hasSlot && !slot.UpdatedAt.IsZero() {
				started = slot.UpdatedAt
			}
			if preparationLimit > 0 && now.Sub(started) > preparationLimit {
				value.Health = "STALLED"
				value.Detail = "preparation exceeded its expected time; inspect jobs and Ludus"
			}
		case "ASSIGNED":
			if !hasSlot || slot.State != domain.SlotAssigned {
				value.Health = "ATTENTION"
				value.Detail = "reservation and slot state disagree"
			} else if slot.WireGuardIssuedAt == nil || slot.WireGuardPeerID == "" {
				value.Health = "ACCESS_MISSING"
				value.Detail = "assigned lease has no recorded WireGuard peer"
			} else if slot.WireGuardFirstHandshake == nil {
				value.Health = "NEEDS_HANDSHAKE"
				value.Detail = "profile delivered; participant has not completed a handshake"
				if noShowGrace > 0 && now.Sub(*slot.WireGuardIssuedAt) > noShowGrace {
					value.Health = "STALLED"
					value.Detail = "no handshake after the configured grace period"
				}
			} else {
				value.Health = "ACTIVE"
				value.Detail = "WireGuard handshake recorded"
			}
		case "DRAINING":
			value.Health = "CLEANUP"
			value.Detail = "revoking access and restoring the clean baseline"
			job, ok := latestJob[reservation.AssignedSlot]
			if !ok {
				value.Health = "STALLED"
				value.Detail = "draining reservation has no lifecycle job"
			} else if job.State == "FAILED" {
				value.Health = "CLEANUP_FAILED"
				value.Detail = fmt.Sprintf("job %d failed: %s", job.ID, job.LastError)
			}
		default:
			value.Health = "ATTENTION"
			value.Detail = "unexpected active reservation state"
		}
		result = append(result, value)
	}
	return result
}
