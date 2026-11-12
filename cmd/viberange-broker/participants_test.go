package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

func TestParticipantHealthList(t *testing.T) {
	now := time.Now().UTC()
	issued := now.Add(-time.Minute)
	handshake := now.Add(-30 * time.Second)
	leaseEnd := now.Add(time.Hour)
	reservations := []domain.Reservation{
		{DiscordUserID: "queued", State: "QUEUED", QueuePosition: 2, RequestedAt: now},
		{DiscordUserID: "needs", State: "ASSIGNED", AssignedSlot: "user1", LeaseEnd: &leaseEnd},
		{DiscordUserID: "active", State: "ASSIGNED", AssignedSlot: "user2", LeaseEnd: &leaseEnd},
		{DiscordUserID: "draining", State: "DRAINING", AssignedSlot: "user3"},
	}
	slots := []domain.Slot{
		{ID: "user1", State: domain.SlotAssigned, WireGuardPeerID: "peer1", WireGuardIssuedAt: &issued},
		{ID: "user2", State: domain.SlotAssigned, WireGuardPeerID: "peer2", WireGuardIssuedAt: &issued, WireGuardFirstHandshake: &handshake},
		{ID: "user3", State: domain.SlotDraining},
	}
	jobs := []domain.Job{{ID: 9, SlotID: "user3", State: "FAILED", LastError: "reset failed"}}
	values := participantHealthList(reservations, slots, jobs, map[string]string{"active": "Alice"}, now, 15*time.Minute, 10*time.Minute)
	if values[0].Health != "WAITING" || values[1].Health != "NEEDS_HANDSHAKE" || values[2].Health != "ACTIVE" || values[2].DisplayName != "Alice" {
		t.Fatalf("health values=%+v", values)
	}
	if values[3].Health != "CLEANUP_FAILED" || !strings.Contains(values[3].Detail, "job 9") {
		t.Fatalf("draining=%+v", values[3])
	}
}

func TestParticipantHealthFlagsStallsAndMismatches(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	issued := old
	values := participantHealthList(
		[]domain.Reservation{
			{DiscordUserID: "preparing", State: "PREPARING", AssignedSlot: "user1", RequestedAt: old},
			{DiscordUserID: "overdue", State: "ASSIGNED", AssignedSlot: "user2"},
		},
		[]domain.Slot{
			{ID: "user1", State: domain.SlotPreparing, UpdatedAt: old},
			{ID: "user2", State: domain.SlotAssigned, WireGuardPeerID: "peer", WireGuardIssuedAt: &issued},
		}, nil, nil, now, 15*time.Minute, 10*time.Minute,
	)
	if values[0].Health != "STALLED" || values[1].Health != "STALLED" {
		t.Fatalf("values=%+v", values)
	}
}
