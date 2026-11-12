package ludus

import (
	"context"
	"errors"
	"time"
)

// Client is the compatibility seam for the Ludus 2.x REST API. The broker core
// deliberately does not import Ludus server internals, allowing an official
// 2.4 plugin or SDK adapter to be added without rewriting lifecycle logic.
type Client interface {
	Version(context.Context) (Version, error)
	Inventory(context.Context, string) (Range, error)
	ValidateBlueprint(context.Context, string) (string, error)
	WireGuardHealth(context.Context) (WireGuardCapability, error)
	IssueWireGuard(context.Context, string, int64) (*WireGuardAccess, error)
	RevokeWireGuard(context.Context, string, int64) (WireGuardPeer, error)
	WireGuardStatus(context.Context, string) (WireGuardPeer, error)
	ResetRange(context.Context, string) error
	VerifyRange(context.Context, string) (Readiness, error)
}

// WireGuardAccess deliberately keeps the client configuration private and
// provides no String, JSON, or database serialization surface.
type WireGuardAccess struct {
	Peer    WireGuardPeer
	profile []byte
}

func NewWireGuardAccess(peer WireGuardPeer, profile []byte) *WireGuardAccess {
	return &WireGuardAccess{Peer: peer, profile: append([]byte(nil), profile...)}
}

func (a *WireGuardAccess) ConsumeProfile() ([]byte, error) {
	if a == nil || len(a.profile) == 0 {
		return nil, errors.New("WireGuard profile is unavailable or already consumed")
	}
	profile := a.profile
	a.profile = nil
	return profile, nil
}

func (a *WireGuardAccess) Destroy() {
	if a == nil {
		return
	}
	for index := range a.profile {
		a.profile[index] = 0
	}
	a.profile = nil
}

type WireGuardCapability struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
}

type WireGuardPeer struct {
	UserID          string     `json:"userID"`
	Generation      int64      `json:"generation"`
	PeerID          string     `json:"peerID"`
	IssuedAt        time.Time  `json:"issuedAt"`
	Revoked         bool       `json:"revoked"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
	ClientIP        string     `json:"clientIP,omitempty"`
	AllowedRange    string     `json:"allowedRange,omitempty"`
	LatestHandshake *time.Time `json:"latestHandshake,omitempty"`
}

type Version struct {
	Server string `json:"server"`
	API    string `json:"api"`
}

type Range struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Testing bool   `json:"testing"`
	VMs     []VM   `json:"vms"`
}

type VM struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	PoweredOn bool   `json:"powered_on"`
}

type Readiness struct {
	Ready     bool      `json:"ready"`
	CheckedAt time.Time `json:"checked_at"`
	Failures  []string  `json:"failures,omitempty"`
}
