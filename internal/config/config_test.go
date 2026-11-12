package config

import (
	"strings"
	"testing"
)

func TestValidateServeAcceptsFullyConfiguredProduction(t *testing.T) {
	cfg := Config{
		Mode:                     "production",
		DiscordGuildID:           "guild",
		DiscordChannelID:         "channel",
		DiscordParticipantRoleID: "role",
		LudusURL:                 "https://ludus.example",
		LudusBlueprintSHA256:     "blueprint-hash",
		LudusTLSCertSHA256:       "certificate-hash",
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe() error = %v", err)
	}
}

func TestValidateServeRejectsProductionWithoutBlueprintGuard(t *testing.T) {
	cfg := Config{
		Mode:                     "production",
		DiscordGuildID:           "guild",
		DiscordChannelID:         "channel",
		DiscordParticipantRoleID: "role",
		LudusURL:                 "https://ludus.example",
		LudusTLSCertSHA256:       "certificate-hash",
	}
	err := cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "VBR_LUDUS_BLUEPRINT_SHA256") {
		t.Fatalf("ValidateServe() error = %v, want blueprint guard error", err)
	}
}

func TestValidateServeAcceptsCompletePerSlotBlueprintGuard(t *testing.T) {
	cfg := Config{
		Mode: "production", DiscordGuildID: "guild", DiscordChannelID: "channel",
		DiscordParticipantRoleID: "role", LudusURL: "https://ludus.example",
		LudusTLSCertSHA256: "certificate-hash", SlotIDs: []string{"user1", "user2"},
		LudusBlueprintSHA256BySlot: map[string]string{"user1": "hash1", "user2": "hash2"},
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe() error = %v", err)
	}
	delete(cfg.LudusBlueprintSHA256BySlot, "user2")
	if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "BY_SLOT") {
		t.Fatalf("ValidateServe() error = %v, want incomplete map rejection", err)
	}
}

func TestSplitMap(t *testing.T) {
	got := splitMap("user1=aaa, user2 = bbb,invalid")
	if got["user1"] != "aaa" || got["user2"] != "bbb" || len(got) != 2 {
		t.Fatalf("splitMap() = %#v", got)
	}
}

func TestValidateServeAcceptsConfiguredSimulation(t *testing.T) {
	cfg := Config{Mode: "simulation", DiscordGuildID: "guild", DiscordChannelID: "channel"}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe() error = %v", err)
	}
}
