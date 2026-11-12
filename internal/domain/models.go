package domain

import "time"

type Slot struct {
	ID                      string
	LudusUser               string
	RangeID                 string
	State                   SlotState
	Generation              int64
	LeaseOwner              string
	LeaseStart              *time.Time
	LeaseEnd                *time.Time
	WireGuardPeerID         string
	WireGuardIssuedAt       *time.Time
	WireGuardRevokedAt      *time.Time
	WireGuardFirstHandshake *time.Time
	UpdatedAt               time.Time
}

type Reservation struct {
	ID            int64
	DiscordUserID string
	State         string
	RequestedAt   time.Time
	AssignedSlot  string
	PreferredSlot string
	LeaseStart    *time.Time
	LeaseEnd      *time.Time
	QueuePosition int
}

type Assignment struct {
	ReservationID int64
	DiscordUserID string
	SlotID        string
	Generation    int64
	LeaseStart    time.Time
	LeaseEnd      time.Time
}

type AuditEvent struct {
	ID            int64
	OccurredAt    time.Time
	Actor         string
	SlotID        string
	ReservationID *int64
	Action        string
	Result        string
	Detail        string
}

type Cooldown struct {
	DiscordUserID string
	Until         time.Time
	Reason        string
}

type Job struct {
	ID             int64
	IdempotencyKey string
	SlotID         string
	Generation     int64
	Transition     string
	State          string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
