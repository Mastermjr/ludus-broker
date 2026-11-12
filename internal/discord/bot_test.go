package discord

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type fakeProductionLudus struct {
	resets         int
	issued         int
	revocations    int
	statusQueries  int
	issuedPeer     ludus.WireGuardPeer
	statusOverride *ludus.WireGuardPeer
}

func (f *fakeProductionLudus) Version(context.Context) (ludus.Version, error) {
	return ludus.Version{}, nil
}
func (f *fakeProductionLudus) Inventory(context.Context, string) (ludus.Range, error) {
	return ludus.Range{ID: "user1", State: "SUCCESS", Testing: true}, nil
}
func (f *fakeProductionLudus) ValidateBlueprint(context.Context, string) (string, error) {
	return "canonical", nil
}
func (f *fakeProductionLudus) WireGuardHealth(context.Context) (ludus.WireGuardCapability, error) {
	return ludus.WireGuardCapability{Name: "viberange-wireguard", Version: "test", Ready: true}, nil
}
func (f *fakeProductionLudus) IssueWireGuard(_ context.Context, userID string, generation int64) (*ludus.WireGuardAccess, error) {
	f.issued++
	f.issuedPeer = ludus.WireGuardPeer{
		UserID: userID, Generation: generation, PeerID: "sha256:peer", IssuedAt: time.Now().UTC(),
	}
	return ludus.NewWireGuardAccess(f.issuedPeer, []byte("[Interface]\nPrivateKey = participant-private\nAddress = 198.51.100.6/32\n")), nil
}
func (f *fakeProductionLudus) RevokeWireGuard(_ context.Context, userID string, generation int64) (ludus.WireGuardPeer, error) {
	f.revocations++
	now := time.Now().UTC()
	return ludus.WireGuardPeer{UserID: userID, Generation: generation, PeerID: "sha256:peer", Revoked: true, RevokedAt: &now}, nil
}
func (f *fakeProductionLudus) WireGuardStatus(_ context.Context, userID string) (ludus.WireGuardPeer, error) {
	f.statusQueries++
	if f.statusOverride != nil {
		return *f.statusOverride, nil
	}
	peer := f.issuedPeer
	peer.UserID = userID
	peer.ClientIP = "198.51.100.6/32"
	peer.AllowedRange = "10.10.0.0/16"
	return peer, nil
}
func (f *fakeProductionLudus) ResetRange(context.Context, string) error {
	f.resets++
	return nil
}
func (f *fakeProductionLudus) VerifyRange(context.Context, string) (ludus.Readiness, error) {
	return ludus.Readiness{Ready: true}, nil
}

func TestProductionPreparationAndPrivateHandoff(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	client := &fakeProductionLudus{}
	var deliveredProfile []byte
	bot := &Bot{
		store: database, ludus: client, mode: "production", leaseDuration: 2 * time.Hour,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		eligibilityOverride: func(string) (bool, error) { return true, nil },
		dmReadinessOverride: func(string) error { return nil },
		productionHandoffOverride: func(_ *domain.Assignment, profile []byte) error {
			deliveredProfile = append([]byte(nil), profile...)
			return nil
		},
	}
	bot.dispatchProduction(ctx)
	reservation, err := database.ActiveReservation(ctx, "discord-user")
	if err != nil || reservation.State != "ASSIGNED" || reservation.LeaseEnd == nil {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if !strings.Contains(string(deliveredProfile), "PrivateKey = participant-private") || client.resets != 1 || client.issued != 1 || client.statusQueries != 1 || client.revocations != 0 {
		t.Fatalf("profile=%v resets=%d issued=%d statuses=%d revocations=%d", len(deliveredProfile) > 0, client.resets, client.issued, client.statusQueries, client.revocations)
	}
}

func TestProductionPostIssueMetadataMismatchRevokesAndCancels(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	client := &fakeProductionLudus{}
	bot := &Bot{
		store: database, ludus: client, mode: "production", leaseDuration: 2 * time.Hour,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		eligibilityOverride: func(string) (bool, error) { return true, nil },
		dmReadinessOverride: func(string) error { return nil },
		productionHandoffOverride: func(*domain.Assignment, []byte) error {
			t.Fatal("handoff ran with mismatched metadata")
			return nil
		},
	}
	bad := ludus.WireGuardPeer{UserID: "user1", Generation: 1, PeerID: "wrong-peer", IssuedAt: time.Now().UTC(), ClientIP: "198.51.100.4/32", AllowedRange: "10.6.0.0/16"}
	client.statusOverride = &bad
	bot.dispatchProduction(ctx)
	if _, err := database.ActiveReservation(ctx, "discord-user"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("active reservation error=%v", err)
	}
	if client.issued != 1 || client.statusQueries != 1 || client.revocations != 1 || client.resets != 2 {
		t.Fatalf("issued=%d statuses=%d revocations=%d resets=%d", client.issued, client.statusQueries, client.revocations, client.resets)
	}
}

func TestProductionDMFailureRevokesResetsAndCancels(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	client := &fakeProductionLudus{}
	bot := &Bot{
		store: database, ludus: client, mode: "production", leaseDuration: 2 * time.Hour,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		eligibilityOverride: func(string) (bool, error) { return true, nil },
		dmReadinessOverride: func(string) error { return nil },
		productionHandoffOverride: func(*domain.Assignment, []byte) error {
			return errors.New("DM blocked")
		},
	}
	bot.dispatchProduction(ctx)
	reservation, err := database.ActiveReservation(ctx, "discord-user")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reservation=%+v err=%v, want cancelled", reservation, err)
	}
	slots, err := database.ListSlots(ctx)
	if err != nil || slots[0].State != domain.SlotAvailable {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	if client.issued != 1 || client.revocations != 1 || client.resets != 2 {
		t.Fatalf("resets=%d issued=%d revocations=%d", client.resets, client.issued, client.revocations)
	}
}

func TestProductionCancelsBeforeMutationWhenDMsUnavailable(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, []string{"user1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Enqueue(ctx, "discord-user"); err != nil {
		t.Fatal(err)
	}
	client := &fakeProductionLudus{}
	bot := &Bot{
		store: database, ludus: client, mode: "production", leaseDuration: 2 * time.Hour,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		eligibilityOverride: func(string) (bool, error) { return true, nil },
		dmReadinessOverride: func(string) error { return errors.New("DMs disabled") },
	}
	bot.dispatchProduction(ctx)
	if _, err := database.ActiveReservation(ctx, "discord-user"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reservation remained active: %v", err)
	}
	if client.resets != 0 || client.issued != 0 || client.revocations != 0 {
		t.Fatalf("external mutation occurred: resets=%d issued=%d revoked=%d", client.resets, client.issued, client.revocations)
	}
}

func TestLinuxHandoffAssetsAreScopedAndSecretFree(t *testing.T) {
	peer := ludus.WireGuardPeer{UserID: "user1", ClientIP: "198.51.100.4/32", AllowedRange: "10.6.0.0/16"}
	guide, err := linuxHandoffGuide("user1", peer)
	if err != nil {
		t.Fatal(err)
	}
	script, err := linuxAcceptanceScript("user1", peer)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"guide": guide, "script": script} {
		value := string(data)
		for _, expected := range []string{"user1", "198.51.100.4/32", "10.6.0.0/16", "10.6.10.10"} {
			if !strings.Contains(value, expected) {
				t.Errorf("%s omitted %q", name, expected)
			}
		}
		if strings.Contains(value, "@@") || strings.Contains(value, "participant-private") {
			t.Errorf("%s contains a template token or secret", name)
		}
	}
	if !strings.Contains(string(guide), "10.6.20.20:8888") || !strings.Contains(string(guide), "directly allowed only for this WireGuard peer") {
		t.Fatal("guide omitted the assigned Caldera endpoint and scope")
	}
	if strings.Contains(strings.ToLower(string(guide)), "token=") {
		t.Fatal("guide embedded a service token")
	}
	path := filepath.Join(t.TempDir(), "acceptance.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("generated script syntax: %v: %s", err, output)
	}
	if _, err := linuxAcceptanceScript("../../bad", peer); err == nil {
		t.Fatal("unsafe slot was accepted")
	}
}

func TestWindowsHandoffAssetsAreGeneralized(t *testing.T) {
	peer := ludus.WireGuardPeer{UserID: "user4", ClientIP: "198.51.100.7/32", AllowedRange: "10.12.0.0/16"}
	guide, err := windowsHandoffGuide("user4", peer)
	if err != nil {
		t.Fatal(err)
	}
	script, err := windowsAcceptanceScript("user4", peer)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"guide": guide, "script": script} {
		value := string(data)
		for _, expected := range []string{"user4", "198.51.100.7/32", "10.12.0.0/16", "10.12.10.10", "10.12.20.20"} {
			if !strings.Contains(value, expected) {
				t.Errorf("%s omitted %q", name, expected)
			}
		}
		for _, forbidden := range []string{"@@", "viberange-user1", "198.51.100.4/32", "10.6.10.10", "participant-private"} {
			if strings.Contains(value, forbidden) {
				t.Errorf("%s retained non-generalized or secret value %q", name, forbidden)
			}
		}
	}
	if !strings.Contains(string(script), "$ExpectedClientAddress = '198.51.100.7'") {
		t.Fatal("PowerShell script did not derive the host address from handoff metadata")
	}
	if !strings.Contains(string(script), "$DeniedIp = '10.12.30.30'") {
		t.Fatal("PowerShell script did not derive the denied-service host from the assigned range")
	}
	if _, lookupErr := exec.LookPath("pwsh"); lookupErr == nil {
		path := filepath.Join(t.TempDir(), "acceptance.ps1")
		if err := os.WriteFile(path, script, 0o600); err != nil {
			t.Fatal(err)
		}
		parser := `$tokens=$null; $errors=$null; [System.Management.Automation.Language.Parser]::ParseFile($env:VBR_PS_PARSE_PATH, [ref]$tokens, [ref]$errors) | Out-Null; if ($errors.Count -ne 0) { $errors | ForEach-Object { Write-Error $_ }; exit 1 }`
		command := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", parser)
		command.Env = append(os.Environ(), "VBR_PS_PARSE_PATH="+path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generated PowerShell syntax: %v: %s", err, output)
		}
	}
	if _, err := windowsAcceptanceScript("user6", peer); err == nil {
		t.Fatal("unsafe Windows handoff slot was accepted")
	}
}

func TestWindowsHandoffAssetsSupportEverySlot(t *testing.T) {
	cases := []struct {
		slot, client, network, service string
	}{
		{"user1", "198.51.100.4/32", "10.6.0.0/16", "10.6.10.10"},
		{"user2", "198.51.100.5/32", "10.7.0.0/16", "10.7.10.10"},
		{"user3", "198.51.100.6/32", "10.10.0.0/16", "10.10.10.10"},
		{"user4", "198.51.100.7/32", "10.12.0.0/16", "10.12.10.10"},
		{"user5", "198.51.100.8/32", "10.13.0.0/16", "10.13.10.10"},
	}
	for _, test := range cases {
		t.Run(test.slot, func(t *testing.T) {
			peer := ludus.WireGuardPeer{UserID: test.slot, ClientIP: test.client, AllowedRange: test.network}
			for name, generate := range map[string]func(string, ludus.WireGuardPeer) ([]byte, error){
				"guide":  windowsHandoffGuide,
				"script": windowsAcceptanceScript,
			} {
				asset, err := generate(test.slot, peer)
				if err != nil {
					t.Fatal(err)
				}
				value := string(asset)
				for _, expected := range []string{test.slot, test.client, test.network, test.service} {
					if !strings.Contains(value, expected) {
						t.Errorf("%s omitted %q", name, expected)
					}
				}
				if strings.Contains(value, "@@") {
					t.Errorf("%s retained a template token", name)
				}
			}
		})
	}
}

func TestWireGuardHandoffArchiveContainsOnePlatformBundle(t *testing.T) {
	peer := ludus.WireGuardPeer{UserID: "user2", ClientIP: "198.51.100.5/32", AllowedRange: "10.7.0.0/16"}
	profile := []byte("[Interface]\nPrivateKey = archive-only-secret\nAddress = 198.51.100.5/32\n\n[Peer]\nAllowedIPs = 10.7.0.0/16\n")
	bundle, err := wireGuardHandoffArchive("user2", profile, peer)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]os.FileMode{
		"viberange-user2.conf":             0o600,
		"viberange-user2-linux-setup.md":   0o600,
		"viberange-user2-acceptance.sh":    0o700,
		"viberange-user2-windows-setup.md": 0o600,
		"viberange-user2-acceptance.ps1":   0o700,
	}
	if len(reader.File) != len(expected) {
		t.Fatalf("archive entries=%d, want %d", len(reader.File), len(expected))
	}
	for _, file := range reader.File {
		wantMode, ok := expected[file.Name]
		if !ok {
			t.Errorf("unexpected archive entry %q", file.Name)
			continue
		}
		if file.Mode().Perm() != wantMode {
			t.Errorf("%s mode=%o, want %o", file.Name, file.Mode().Perm(), wantMode)
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		content, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if file.Name == "viberange-user2.conf" {
			if !bytes.Equal(content, profile) {
				t.Fatal("archive profile differs from transient source")
			}
		} else if bytes.Contains(content, []byte("archive-only-secret")) || bytes.Contains(content, []byte("@@")) {
			t.Errorf("%s contains profile secret or template token", file.Name)
		}
	}
}

func TestStatusDescriptionsExplainLifecycle(t *testing.T) {
	leaseEnd := time.Unix(1_800_000_000, 0)
	cases := []struct {
		reservation domain.Reservation
		want        string
	}{
		{domain.Reservation{State: "QUEUED", QueuePosition: 2}, "position **2**"},
		{domain.Reservation{State: "PREPARING", AssignedSlot: "user1"}, "clean baseline"},
		{domain.Reservation{State: "ASSIGNED", AssignedSlot: "user1", LeaseEnd: &leaseEnd}, "assigned **user1**"},
		{domain.Reservation{State: "DRAINING", AssignedSlot: "user1"}, "WireGuard access is being revoked"},
	}
	for _, test := range cases {
		if got := describeReservation(test.reservation); !strings.Contains(got, test.want) {
			t.Errorf("describeReservation(%s)=%q, want %q", test.reservation.State, got, test.want)
		}
	}
	draining := domain.Reservation{State: "DRAINING", AssignedSlot: "user2"}
	quarantined := describeReservationWithSlotState(draining, domain.SlotQuarantined)
	if !strings.Contains(quarantined, "**QUARANTINED**") || !strings.Contains(quarantined, "instructor recovery") || !strings.Contains(quarantined, "remains **DRAINING**") {
		t.Fatalf("quarantined participant status was ambiguous: %q", quarantined)
	}
	resetting := describeReservationWithSlotState(draining, domain.SlotResetting)
	if !strings.Contains(resetting, "**RESETTING**") {
		t.Fatalf("resetting participant status omitted slot state: %q", resetting)
	}
	expiration := formatLeaseExpiration(time.Date(2026, 11, 11, 10, 2, 48, 0, time.UTC))
	if expiration != "<t:1794391368:F> (UTC: 2026-11-11 10:02:48Z)" {
		t.Fatalf("expiration=%q", expiration)
	}
	help := rangeLifecycleHelp()
	for _, state := range []string{"AVAILABLE", "RESERVED", "PREPARING", "READY", "ASSIGNED", "DRAINING", "RESETTING", "VERIFYING", "QUARANTINED"} {
		if !strings.Contains(help, "`"+state+"` —") {
			t.Errorf("lifecycle help omitted newline-delimited %s", state)
		}
	}
	if !strings.Contains(help, "`AVAILABLE` → `RESERVED` → `PREPARING`") {
		t.Fatal("lifecycle help omitted arrow path")
	}
}

func TestInteractionUserID(t *testing.T) {
	tests := []struct {
		name        string
		interaction *discordgo.InteractionCreate
		want        string
		ok          bool
	}{
		{
			name: "guild interaction uses member user",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				Member: &discordgo.Member{User: &discordgo.User{ID: "guild-user"}},
			}},
			want: "guild-user",
			ok:   true,
		},
		{
			name: "direct interaction uses top-level user",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				User: &discordgo.User{ID: "dm-user"},
			}},
			want: "dm-user",
			ok:   true,
		},
		{name: "nil interaction", interaction: nil},
		{name: "missing users", interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := interactionUserID(test.interaction)
			if got != test.want || ok != test.ok {
				t.Fatalf("interactionUserID() = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestSendHandoffRefusesProductionSimulationCredential(t *testing.T) {
	bot := &Bot{mode: "production"}
	err := bot.sendHandoff(&domain.Assignment{})
	if err == nil || !strings.Contains(err.Error(), "refusing simulated WireGuard") {
		t.Fatalf("sendHandoff() error = %v, want fail-closed error", err)
	}
}

func TestEveryInteractionResponseIsEphemeral(t *testing.T) {
	response := ephemeralInteractionResponse("private")
	if response == nil || response.Data == nil {
		t.Fatal("ephemeralInteractionResponse() returned an incomplete response")
	}
	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v", response.Type)
	}
	if response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("response flags = %v, want ephemeral", response.Data.Flags)
	}
}

func TestWrongChannelResponseLinksConfiguredChannel(t *testing.T) {
	bot := &Bot{guildID: "guild", channelID: "target-channel", participantRoleID: "participant"}
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		GuildID: "guild", ChannelID: "wrong-channel", Member: &discordgo.Member{Roles: []string{"participant"}},
	}}
	if reason := bot.ineligibilityReason(interaction); reason != "Use `/range` in <#target-channel>." {
		t.Fatalf("reason = %q", reason)
	}
}

func TestCommandRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)
	bot := &Bot{commandInterval: time.Second, now: func() time.Time { return now }}
	if bot.commandRateLimited("user") {
		t.Fatal("first command was rate limited")
	}
	if !bot.commandRateLimited("user") {
		t.Fatal("immediate repeated command was not rate limited")
	}
	if bot.commandRateLimited("other-user") {
		t.Fatal("one user rate limited another user")
	}
	now = now.Add(time.Second)
	if bot.commandRateLimited("user") {
		t.Fatal("command remained rate limited after interval")
	}
}

func TestEligibility(t *testing.T) {
	bot := &Bot{guildID: "guild", channelID: "channel", participantRoleID: "participant", mode: "production"}
	tests := []struct {
		name        string
		interaction *discordgo.InteractionCreate
		allowed     bool
	}{
		{
			name: "eligible member",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				GuildID: "guild", ChannelID: "channel", Member: &discordgo.Member{Roles: []string{"participant"}},
			}},
			allowed: true,
		},
		{
			name: "wrong guild",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				GuildID: "other", ChannelID: "channel", Member: &discordgo.Member{Roles: []string{"participant"}},
			}},
		},
		{
			name: "wrong channel",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				GuildID: "guild", ChannelID: "other", Member: &discordgo.Member{Roles: []string{"participant"}},
			}},
		},
		{
			name: "missing role",
			interaction: &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				GuildID: "guild", ChannelID: "channel", Member: &discordgo.Member{Roles: []string{"other"}},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowed := bot.ineligibilityReason(test.interaction) == ""
			if allowed != test.allowed {
				t.Fatalf("allowed=%v, want %v", allowed, test.allowed)
			}
		})
	}
}
