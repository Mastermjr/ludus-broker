package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	brokerdiscord "github.com/Mastermjr/ludus-viberange-broker/internal/discord"
	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
)

type fakeStartupValidator struct {
	ready    bool
	failSlot string
	checked  []string
}

func (f *fakeStartupValidator) WireGuardHealth(context.Context) (ludus.WireGuardCapability, error) {
	return ludus.WireGuardCapability{Name: "viberange-wireguard", Version: "1.0.0", Ready: f.ready}, nil
}

func (f *fakeStartupValidator) ValidateBlueprint(_ context.Context, slot string) (string, error) {
	f.checked = append(f.checked, slot)
	if slot == f.failSlot {
		return "", errors.New("hash mismatch")
	}
	return "approved-" + slot, nil
}

func TestAdministrativeJobRequiresConfirmationAndIsIdempotent(t *testing.T) {
	t.Setenv("VBR_DATABASE_PATH", filepath.Join(t.TempDir(), "broker.db"))
	t.Setenv("VBR_SLOT_IDS", "user1")
	t.Setenv("VBR_MODE", "simulation")

	if err := run([]string{"reset", "user1"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("reset without confirmation error = %v", err)
	}
	if err := run([]string{"reset", "user1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"reset", "user1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"reset", "missing", "--yes"}); err == nil {
		t.Fatal("missing slot reset unexpectedly succeeded")
	}
}

func TestProductionStartupValidatesEveryBlueprintBeforeServing(t *testing.T) {
	validator := &fakeStartupValidator{ready: true}
	_, hashes, err := validateProductionLudus(context.Background(), validator, []string{"user1", "user2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(validator.checked) != 2 || hashes["user2"] != "approved-user2" {
		t.Fatalf("checked=%v hashes=%v", validator.checked, hashes)
	}

	validator = &fakeStartupValidator{ready: true, failSlot: "user2"}
	if _, _, err := validateProductionLudus(context.Background(), validator, []string{"user1", "user2"}); err == nil || !strings.Contains(err.Error(), "user2") {
		t.Fatalf("blueprint mismatch error = %v", err)
	}

	validator = &fakeStartupValidator{}
	if _, _, err := validateProductionLudus(context.Background(), validator, []string{"user1"}); err == nil || len(validator.checked) != 0 {
		t.Fatalf("unready plugin error=%v checked=%v", err, validator.checked)
	}
}

func TestResolveDiscordResources(t *testing.T) {
	resources := brokerdiscord.ResourceDiscovery{
		GuildID:  "guild",
		Channels: []brokerdiscord.DiscoveredChannel{{ID: "channel-id", Name: "queue"}},
		Roles: []brokerdiscord.DiscoveredRole{
			{ID: "guild", Name: "@everyone"},
			{ID: "role-id", Name: "Participant"},
			{ID: "bot-role", Name: "bot", Managed: true},
		},
	}
	if id, err := resolveChannel(resources, "queue"); err != nil || id != "channel-id" {
		t.Fatalf("channel id=%q err=%v", id, err)
	}
	if id, err := resolveRole(resources, "Participant"); err != nil || id != "role-id" {
		t.Fatalf("role id=%q err=%v", id, err)
	}
	if _, err := resolveRole(resources, "@everyone"); err == nil {
		t.Fatal("@everyone accepted as participant role")
	}
	if _, err := resolveRole(resources, "bot"); err == nil {
		t.Fatal("managed role accepted as participant role")
	}
}
