package discord

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Mastermjr/ludus-viberange-broker/internal/app"
	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
)

type Bot struct {
	session                    *discordgo.Session
	store                      *store.Store
	commands                   *app.Service
	ludus                      ludus.Client
	guildID                    string
	channelID                  string
	participantRoleID          string
	leaseDuration              time.Duration
	mode                       string
	log                        *slog.Logger
	rateMu                     sync.Mutex
	lastCommand                map[string]time.Time
	commandInterval            time.Duration
	now                        func() time.Time
	eligibilityOverride        func(string) (bool, error)
	dmReadinessOverride        func(string) error
	productionHandoffOverride  func(*domain.Assignment, []byte) error
	resourceValidationOverride func() error
	resourceRetryInitial       time.Duration
	resourceRetryMaximum       time.Duration
}

func New(tokenFile, guildID, channelID, participantRoleID, mode string, leaseDuration, cooldownDuration time.Duration, database *store.Store, log *slog.Logger, ludusClients ...ludus.Client) (*Bot, error) {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read Discord token: %w", err)
	}
	value := strings.TrimSpace(string(token))
	if value == "" {
		return nil, errors.New("Discord token file is empty")
	}
	session, err := discordgo.New("Bot " + value)
	value = ""
	if err != nil {
		return nil, fmt.Errorf("create Discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsDirectMessages
	var ludusClient ludus.Client
	if len(ludusClients) > 0 {
		ludusClient = ludusClients[0]
	}
	bot := &Bot{
		session:           session,
		store:             database,
		commands:          app.New(database, mode == "simulation", cooldownDuration),
		ludus:             ludusClient,
		guildID:           guildID,
		channelID:         channelID,
		participantRoleID: participantRoleID,
		leaseDuration:     leaseDuration,
		mode:              mode,
		log:               log,
		lastCommand:       make(map[string]time.Time),
		commandInterval:   time.Second,
		now:               time.Now,
	}
	session.AddHandler(bot.onReady)
	session.AddHandler(bot.onInteraction)
	return bot, nil
}

func (b *Bot) Run(ctx context.Context) error {
	skew, err := CheckClock(ctx, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("clock check failed: %w", err)
	}
	b.log.Info("clock check passed", "discord_skew", skew.Round(time.Second))
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open Discord gateway: %w", err)
	}
	defer b.session.Close()
	if err := b.waitForResources(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	if err := b.registerCommands(); err != nil {
		return fmt.Errorf("register Discord commands: %w", err)
	}
	b.log.Info("Discord bot connected", "guild_id", b.guildID, "mode", b.mode)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.dispatch(ctx)
		}
	}
}

func (b *Bot) waitForResources(ctx context.Context) error {
	delay := b.resourceRetryInitial
	if delay <= 0 {
		delay = 5 * time.Second
	}
	maximum := b.resourceRetryMaximum
	if maximum <= 0 {
		maximum = 5 * time.Minute
	}
	for {
		err := b.validateConfiguredResources()
		if err == nil {
			return nil
		}
		b.log.Warn("Discord resources unavailable; retrying without exiting", "retry_in", delay, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < maximum {
			delay *= 2
			if delay > maximum {
				delay = maximum
			}
		}
	}
}

func (b *Bot) validateConfiguredResources() error {
	if b.resourceValidationOverride != nil {
		return b.resourceValidationOverride()
	}
	return b.validateResources()
}

func (b *Bot) validateResources() error {
	channel, err := b.session.Channel(b.channelID)
	if err != nil {
		return fmt.Errorf("load participant channel %s: %w", b.channelID, err)
	}
	if channel.GuildID != b.guildID {
		return fmt.Errorf("participant channel %s does not belong to configured guild %s", b.channelID, b.guildID)
	}
	roles, err := b.session.GuildRoles(b.guildID)
	if err != nil {
		return fmt.Errorf("list guild roles: %w", err)
	}
	if b.participantRoleID == "" {
		if b.mode != "simulation" {
			return errors.New("participant role is required")
		}
		var available []string
		for _, role := range roles {
			if !role.Managed && role.ID != b.guildID {
				available = append(available, fmt.Sprintf("%s=%s", role.Name, role.ID))
			}
		}
		b.log.Warn("participant role checks disabled in simulation mode", "available_roles", available)
		return nil
	}
	for _, role := range roles {
		if role.ID != b.participantRoleID {
			continue
		}
		if role.ID == b.guildID || role.Managed {
			return fmt.Errorf("participant role %s must be a non-managed role other than @everyone", role.ID)
		}
		b.log.Info("Discord resources validated", "guild_id", b.guildID, "channel_id", b.channelID, "participant_role_id", role.ID, "participant_role_name", role.Name)
		return nil
	}
	return fmt.Errorf("participant role %s does not exist in guild %s", b.participantRoleID, b.guildID)
}

func (b *Bot) registerCommands() error {
	dmPermission := false
	contexts := []discordgo.InteractionContextType{discordgo.InteractionContextGuild}
	commands := []*discordgo.ApplicationCommand{{
		Name:         "range",
		Description:  "Reserve and inspect a VibeRange workshop environment",
		DMPermission: &dmPermission,
		Contexts:     &contexts,
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "join", Description: "Join the participant queue"},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "leave", Description: "Leave the queue"},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show your queue or lease status"},
		},
	}}
	_, err := b.session.ApplicationCommandBulkOverwrite(b.session.State.User.ID, b.guildID, commands)
	return err
}

func (b *Bot) onReady(_ *discordgo.Session, ready *discordgo.Ready) {
	b.log.Info("Discord gateway ready", "bot_user", ready.User.Username)
}

func (b *Bot) onInteraction(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error("recovered Discord interaction panic", "panic", recovered)
			if session != nil && interaction != nil && interaction.Interaction != nil {
				b.respond(session, interaction, "The command failed unexpectedly. Please try again.")
			}
		}
	}()
	if interaction == nil || interaction.Interaction == nil || interaction.Type != discordgo.InteractionApplicationCommand || interaction.ApplicationCommandData().Name != "range" {
		return
	}
	options := interaction.ApplicationCommandData().Options
	if len(options) != 1 {
		b.respond(session, interaction, "Invalid command.")
		return
	}
	userID, ok := interactionUserID(interaction)
	if !ok {
		b.log.Error("Discord interaction did not identify its user", "guild_id", interaction.GuildID)
		b.respond(session, interaction, "Discord did not identify the requesting user. Please try again.")
		return
	}
	if reason := b.ineligibilityReason(interaction); reason != "" {
		b.log.Warn("Discord range command denied", "reason", reason, "guild_id", interaction.GuildID, "channel_id", interaction.ChannelID)
		b.respond(session, interaction, reason)
		return
	}
	if b.commandRateLimited(userID) {
		b.respond(session, interaction, "Please wait a moment before using another `/range` command.")
		return
	}
	b.log.Info("Discord range command received", "subcommand", options[0].Name, "guild_id", interaction.GuildID)
	var message string
	switch options[0].Name {
	case "join":
		message = b.join(context.Background(), userID)
	case "leave":
		message = b.leave(context.Background(), userID)
	case "status":
		message = b.status(context.Background(), userID)
	default:
		message = "Unknown `/range` subcommand."
	}
	b.respond(session, interaction, message)
}

func (b *Bot) ineligibilityReason(interaction *discordgo.InteractionCreate) string {
	if interaction.GuildID == "" || interaction.GuildID != b.guildID {
		return "This command is only available in the configured workshop server."
	}
	if interaction.ChannelID != b.channelID {
		return fmt.Sprintf("Use `/range` in <#%s>.", b.channelID)
	}
	if b.participantRoleID == "" {
		if b.mode == "simulation" {
			return ""
		}
		return "The participant role is not configured; ask an instructor."
	}
	if interaction.Member == nil || !hasRole(interaction.Member.Roles, b.participantRoleID) {
		return "You do not have the workshop participant role."
	}
	return ""
}

func (b *Bot) commandRateLimited(userID string) bool {
	b.rateMu.Lock()
	defer b.rateMu.Unlock()
	if b.lastCommand == nil {
		b.lastCommand = make(map[string]time.Time)
	}
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	interval := b.commandInterval
	if interval <= 0 {
		interval = time.Second
	}
	if last, ok := b.lastCommand[userID]; ok && now.Sub(last) < interval {
		return true
	}
	b.lastCommand[userID] = now
	return false
}

func hasRole(roles []string, wanted string) bool {
	for _, role := range roles {
		if role == wanted {
			return true
		}
	}
	return false
}

func interactionUserID(interaction *discordgo.InteractionCreate) (string, bool) {
	if interaction != nil && interaction.Interaction != nil {
		if interaction.Member != nil && interaction.Member.User != nil && interaction.Member.User.ID != "" {
			return interaction.Member.User.ID, true
		}
		if interaction.User != nil && interaction.User.ID != "" {
			return interaction.User.ID, true
		}
	}
	return "", false
}

func (b *Bot) join(ctx context.Context, userID string) string {
	reservation, created, err := b.commands.Join(ctx, userID)
	var cooldown app.CooldownError
	if errors.As(err, &cooldown) {
		return fmt.Sprintf("You can rejoin the queue after <t:%d:F>.", cooldown.Until.Unix())
	}
	if err != nil {
		b.log.Error("queue join failed", "error", err)
		return "I could not add you to the queue. Please try again."
	}
	if !created {
		return describeReservation(reservation)
	}
	return fmt.Sprintf("You joined the VibeRange queue at position **%d**. Ensure server-member DMs are enabled; I will privately send your WireGuard file when a range is ready.", reservation.QueuePosition)
}

func (b *Bot) leave(ctx context.Context, userID string) string {
	state, err := b.commands.Leave(ctx, userID)
	if err != nil {
		b.log.Error("queue leave failed", "error", err)
		return "I could not remove you from the queue. Please try again."
	}
	switch state {
	case "CANCELLED":
		return "You left the VibeRange queue."
	case "RELEASED":
		return "Your simulated lease ended and the PoC slot is available again."
	case "RELEASING":
		return "Your lease is being released. WireGuard access will be revoked before the range is reset."
	default:
		return "You are not currently in the queue."
	}
}

func (b *Bot) status(ctx context.Context, userID string) string {
	reservation, err := b.commands.Status(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return "You have no active queue entry or range lease. Use `/range join` to enter the queue.\n\n" + rangeLifecycleHelp()
	}
	if err != nil {
		b.log.Error("reservation status failed", "error", err)
		return "The queue is temporarily unavailable. Please try again."
	}
	if reservation.AssignedSlot != "" {
		slotState, slotErr := b.store.SlotState(ctx, reservation.AssignedSlot)
		if slotErr != nil {
			b.log.Error("reservation slot status failed", "slot", reservation.AssignedSlot, "error", slotErr)
			return "The queue is temporarily unavailable. Please try again."
		}
		return describeReservationWithSlotState(reservation, slotState) + "\n\n" + rangeLifecycleHelp()
	}
	return describeReservation(reservation) + "\n\n" + rangeLifecycleHelp()
}

func describeReservationWithSlotState(reservation domain.Reservation, slotState domain.SlotState) string {
	switch slotState {
	case domain.SlotQuarantined:
		return fmt.Sprintf("**%s** is **QUARANTINED**: cleanup could not be proven and instructor recovery is required. Your reservation remains **DRAINING** until recovery completes.", reservation.AssignedSlot)
	case domain.SlotResetting:
		return fmt.Sprintf("**%s** is **RESETTING**: WireGuard access has been revoked and the clean baseline is being restored.", reservation.AssignedSlot)
	case domain.SlotVerifying:
		return fmt.Sprintf("**%s** is **VERIFYING**: the restored baseline is undergoing safety checks.", reservation.AssignedSlot)
	default:
		return describeReservation(reservation)
	}
}

func describeReservation(reservation domain.Reservation) string {
	if reservation.State == "QUEUED" {
		return fmt.Sprintf("You are position **%d** in the VibeRange queue.", reservation.QueuePosition)
	}
	if reservation.State == "PREPARING" {
		return fmt.Sprintf("**%s** is **PREPARING**: the clean baseline and private WireGuard access are being prepared.", reservation.AssignedSlot)
	}
	if reservation.State == "ASSIGNED" && reservation.LeaseEnd != nil {
		return fmt.Sprintf("You are assigned **%s** until %s.", reservation.AssignedSlot, formatLeaseExpiration(*reservation.LeaseEnd))
	}
	if reservation.State == "DRAINING" {
		return fmt.Sprintf("**%s** is **DRAINING**: WireGuard access is being revoked and the clean baseline restored.", reservation.AssignedSlot)
	}
	return fmt.Sprintf("Your reservation state is **%s**.", reservation.State)
}

func formatLeaseExpiration(value time.Time) string {
	utc := value.UTC()
	return fmt.Sprintf("<t:%d:F> (UTC: %s)", utc.Unix(), utc.Format("2006-01-02 15:04:05Z"))
}

func rangeLifecycleHelp() string {
	return "**Range lifecycle**\n" +
		"`AVAILABLE` — ready\n" +
		"`RESERVED` — selected\n" +
		"`PREPARING` — reset and access setup\n" +
		"`READY` — handoff ready\n" +
		"`ASSIGNED` — active lease\n" +
		"`DRAINING` — revoking access\n" +
		"`RESETTING` — restoring baseline\n" +
		"`VERIFYING` — safety checks\n" +
		"`QUARANTINED` — instructor attention required\n\n" +
		"**Normal path**\n" +
		"`AVAILABLE` → `RESERVED` → `PREPARING` → `READY` → `ASSIGNED` → `DRAINING` → `RESETTING` → `VERIFYING` → `AVAILABLE`\n" +
		"Failures move the range to `QUARANTINED`."
}

func ephemeralInteractionResponse(content string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	}
}

func (b *Bot) respond(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) {
	err := session.InteractionRespond(interaction.Interaction, ephemeralInteractionResponse(content))
	if err != nil {
		b.log.Error("Discord interaction response failed", "error", err)
	}
}

func (b *Bot) dispatch(ctx context.Context) {
	if b.mode == "production" {
		b.dispatchProduction(ctx)
		return
	}
	assignment, err := b.store.AssignNext(ctx, b.leaseDuration)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		b.log.Error("queue dispatch failed", "error", err)
		return
	}
	eligible, eligibilityErr := b.eligibleForHandoff(assignment.DiscordUserID)
	if eligibilityErr != nil {
		b.log.Error("Discord eligibility revalidation failed; returning participant to queue", "slot", assignment.SlotID, "error", eligibilityErr)
		if retryErr := b.store.RetryAssignment(ctx, *assignment); retryErr != nil {
			b.log.Error("assignment recovery failed", "slot", assignment.SlotID, "error", retryErr)
		}
		return
	}
	if !eligible {
		b.log.Warn("Discord assignment denied after eligibility revalidation", "slot", assignment.SlotID)
		if rejectErr := b.store.RejectAssignment(ctx, *assignment, "participant left guild or lost participant role"); rejectErr != nil {
			b.log.Error("assignment rejection failed", "slot", assignment.SlotID, "error", rejectErr)
		}
		return
	}
	if err := b.sendHandoff(assignment); err != nil {
		b.log.Error("Discord handoff failed; cancelling assignment", "slot", assignment.SlotID, "error", err)
		if rejectErr := b.store.RejectAssignment(ctx, *assignment, "private DM delivery failed"); rejectErr != nil {
			b.log.Error("assignment cancellation failed", "slot", assignment.SlotID, "error", rejectErr)
		}
		return
	}
	b.log.Info("participant handoff delivered", "slot", assignment.SlotID, "lease_end", assignment.LeaseEnd)
}

func (b *Bot) dispatchProduction(ctx context.Context) {
	assignment, err := b.store.PrepareNext(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		b.log.Error("production preparation claim failed", "error", err)
		return
	}
	eligible, err := b.eligibleForHandoff(assignment.DiscordUserID)
	if err != nil {
		b.log.Error("production eligibility revalidation failed", "slot", assignment.SlotID, "error", err)
		b.endPreparation(ctx, assignment, "QUEUED", domain.SlotAvailable, "temporary Discord eligibility lookup failure")
		return
	}
	if !eligible {
		b.log.Warn("production assignment denied after eligibility revalidation", "slot", assignment.SlotID)
		b.endPreparation(ctx, assignment, "CANCELLED", domain.SlotAvailable, "participant left guild or lost participant role")
		return
	}
	if err := b.sendPreparationNotice(assignment.DiscordUserID); err != nil {
		b.log.Warn("production assignment cancelled because participant DMs are unavailable", "slot", assignment.SlotID, "error", err)
		b.endPreparation(ctx, assignment, "CANCELLED", domain.SlotAvailable, "participant DMs unavailable")
		return
	}
	if b.ludus == nil {
		b.log.Error("production Ludus client is unavailable", "slot", assignment.SlotID)
		b.endPreparation(ctx, assignment, "CANCELLED", domain.SlotQuarantined, "Ludus client unavailable")
		return
	}
	if err := b.ludus.ResetRange(ctx, assignment.SlotID); err != nil {
		b.log.Error("production range preparation failed", "slot", assignment.SlotID, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, false, err)
		return
	}
	access, err := b.ludus.IssueWireGuard(ctx, assignment.SlotID, assignment.Generation)
	if err != nil {
		b.log.Error("production WireGuard issue failed", "slot", assignment.SlotID, "generation", assignment.Generation, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, false, err)
		return
	}
	defer access.Destroy()
	if access.Peer.ClientIP == "" || access.Peer.AllowedRange == "" {
		peer, statusErr := b.verifiedIssuedPeerStatus(ctx, assignment, access.Peer)
		if statusErr != nil {
			b.log.Error("production WireGuard post-issue metadata verification failed", "slot", assignment.SlotID, "generation", assignment.Generation, "error", statusErr)
			b.rollbackExternalPreparation(ctx, assignment, true, statusErr)
			return
		}
		access.Peer = peer
	}
	if err := b.store.RecordWireGuardIssued(ctx, *assignment, access.Peer.PeerID, access.Peer.IssuedAt); err != nil {
		b.log.Error("production WireGuard metadata commit failed", "slot", assignment.SlotID, "generation", assignment.Generation, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, true, err)
		return
	}
	profile, err := access.ConsumeProfile()
	if err != nil {
		b.log.Error("production WireGuard profile unavailable", "slot", assignment.SlotID, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, true, err)
		return
	}
	defer zero(profile)
	assignment.LeaseStart = time.Now().UTC()
	assignment.LeaseEnd = assignment.LeaseStart.Add(b.leaseDuration)
	if err := b.sendProductionHandoff(assignment, profile, access.Peer); err != nil {
		b.log.Error("production Discord DM attachment failed", "slot", assignment.SlotID, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, true, err)
		return
	}
	if err := b.store.CompletePreparation(ctx, *assignment); err != nil {
		b.log.Error("production handoff state commit failed", "slot", assignment.SlotID, "error", err)
		b.rollbackExternalPreparation(ctx, assignment, true, err)
		return
	}
	b.log.Info("production participant handoff delivered", "slot", assignment.SlotID, "lease_end", assignment.LeaseEnd)
}

func (b *Bot) endPreparation(ctx context.Context, assignment *domain.Assignment, reservationState string, slotState domain.SlotState, reason string) {
	if err := b.store.EndPreparation(ctx, *assignment, reservationState, slotState); err != nil {
		b.log.Error("end production preparation failed", "slot", assignment.SlotID, "reason", reason, "error", err)
	}
}

func (b *Bot) rollbackExternalPreparation(ctx context.Context, assignment *domain.Assignment, peerMayHaveBeenIssued bool, cause error) {
	rollbackOK := true
	if peerMayHaveBeenIssued {
		peer, err := b.ludus.RevokeWireGuard(ctx, assignment.SlotID, assignment.Generation)
		if err != nil || !peer.Revoked {
			rollbackOK = false
			b.log.Error("production rollback WireGuard revocation failed", "slot", assignment.SlotID, "generation", assignment.Generation, "error", err)
		} else if peer.RevokedAt != nil {
			if err := b.store.RecordWireGuardRevoked(ctx, assignment.SlotID, assignment.Generation, *peer.RevokedAt); err != nil {
				rollbackOK = false
				b.log.Error("production rollback revocation metadata failed", "slot", assignment.SlotID, "error", err)
			}
		}
	}
	if err := b.ludus.ResetRange(ctx, assignment.SlotID); err != nil {
		rollbackOK = false
		b.log.Error("production rollback range reset failed", "slot", assignment.SlotID, "error", err)
	}
	if rollbackOK {
		b.endPreparation(ctx, assignment, "CANCELLED", domain.SlotAvailable, cause.Error())
		b.sendPreparationFailureNotice(assignment.DiscordUserID)
		return
	}
	b.endPreparation(ctx, assignment, "CANCELLED", domain.SlotQuarantined, cause.Error())
}

func (b *Bot) sendPreparationNotice(userID string) error {
	if b.dmReadinessOverride != nil {
		return b.dmReadinessOverride(userID)
	}
	channel, err := b.session.UserChannelCreate(userID)
	if err != nil {
		return err
	}
	_, err = b.session.ChannelMessageSend(channel.ID, "Your VibeRange assignment is being prepared. I will send one private ZIP containing your WireGuard profile and Linux/Windows setup files when it is ready.")
	return err
}

func (b *Bot) sendPreparationFailureNotice(userID string) {
	if b.session == nil {
		return
	}
	channel, err := b.session.UserChannelCreate(userID)
	if err != nil {
		return
	}
	_, _ = b.session.ChannelMessageSend(channel.ID, "Your VibeRange preparation could not complete safely. The generated access was revoked and the range was reset. Use `/range join` again when you are ready.")
}

func (b *Bot) verifiedIssuedPeerStatus(ctx context.Context, assignment *domain.Assignment, issued ludus.WireGuardPeer) (ludus.WireGuardPeer, error) {
	status, err := b.ludus.WireGuardStatus(ctx, assignment.SlotID)
	if err != nil {
		return ludus.WireGuardPeer{}, fmt.Errorf("fetch post-issue WireGuard status: %w", err)
	}
	if status.UserID != assignment.SlotID || status.Generation != assignment.Generation || status.PeerID == "" || status.PeerID != issued.PeerID {
		return ludus.WireGuardPeer{}, errors.New("post-issue WireGuard status identity does not match the issued peer")
	}
	if status.Revoked {
		return ludus.WireGuardPeer{}, errors.New("post-issue WireGuard status reports the issued peer revoked")
	}
	if issued.IssuedAt.IsZero() || status.IssuedAt.IsZero() || !status.IssuedAt.Equal(issued.IssuedAt) {
		return ludus.WireGuardPeer{}, errors.New("post-issue WireGuard status timestamp does not match the issued peer")
	}
	if status.ClientIP == "" || status.AllowedRange == "" {
		return ludus.WireGuardPeer{}, errors.New("post-issue WireGuard status omitted clientIP or allowedRange")
	}
	if err := validateHandoffMetadata(assignment.SlotID, status); err != nil {
		return ludus.WireGuardPeer{}, fmt.Errorf("verify post-issue WireGuard metadata: %w", err)
	}
	return status, nil
}

func (b *Bot) sendProductionHandoff(assignment *domain.Assignment, profile []byte, peer ludus.WireGuardPeer) error {
	if len(profile) == 0 || !bytes.Contains(profile, []byte("[Interface]")) || !bytes.Contains(profile, []byte("PrivateKey")) {
		return errors.New("Ludus returned an invalid WireGuard profile")
	}
	if b.productionHandoffOverride != nil {
		return b.productionHandoffOverride(assignment, profile)
	}
	return b.sendWireGuardDM(assignment, profile, peer, false)
}

func (b *Bot) sendWireGuardDM(assignment *domain.Assignment, profile []byte, peer ludus.WireGuardPeer, simulated bool) error {
	channel, err := b.session.UserChannelCreate(assignment.DiscordUserID)
	if err != nil {
		return err
	}
	prefix := "Your VibeRange lease is ready."
	if simulated {
		prefix = "Your simulated VibeRange lease is ready. This profile is not usable."
	}
	archive, err := wireGuardHandoffArchive(assignment.SlotID, profile, peer)
	if err != nil {
		return fmt.Errorf("generate WireGuard handoff archive: %w", err)
	}
	defer zero(archive)
	overview, err := handoffOverview(assignment.SlotID, peer)
	if err != nil {
		return fmt.Errorf("generate WireGuard handoff overview: %w", err)
	}
	archiveHash := sha256.Sum256(archive)
	message := fmt.Sprintf("**%s**\n\nSlot: `%s`\nExpires: %s\nZIP SHA-256: `%x`\n\nKeep the ZIP and `.conf` private. Discord can preview the attached Markdown overview; the ZIP contains your profile and complete Linux/Windows setup files. Access stops when your lease ends.", prefix, assignment.SlotID, formatLeaseExpiration(assignment.LeaseEnd), archiveHash)
	_, err = b.session.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Content: message,
		Files: []*discordgo.File{
			{Name: "viberange-" + assignment.SlotID + "-README.md", ContentType: "text/markdown", Reader: bytes.NewReader(overview)},
			{Name: "viberange-" + assignment.SlotID + "-handoff.zip", ContentType: "application/zip", Reader: bytes.NewReader(archive)},
		},
	})
	return err
}

func (b *Bot) eligibleForHandoff(userID string) (bool, error) {
	if b.eligibilityOverride != nil {
		return b.eligibilityOverride(userID)
	}
	member, err := b.session.GuildMember(b.guildID, userID)
	if err != nil {
		var restErr *discordgo.RESTError
		if errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == 404 {
			return false, nil
		}
		return false, err
	}
	if b.participantRoleID == "" && b.mode == "simulation" {
		return true, nil
	}
	return hasRole(member.Roles, b.participantRoleID), nil
}

func (b *Bot) sendHandoff(assignment *domain.Assignment) error {
	if b.mode != "simulation" {
		return errors.New("refusing simulated WireGuard handoff outside simulation mode")
	}
	privateKey := make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(privateKey)
	zero(privateKey)
	profile := []byte(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 198.51.100.254/32\n\n[Peer]\nPublicKey = SIMULATION_ONLY\nEndpoint = 127.0.0.1:9\nAllowedIPs = 10.255.0.0/16\n", encoded))
	defer zero(profile)
	peer := ludus.WireGuardPeer{UserID: assignment.SlotID, ClientIP: "198.51.100.254/32", AllowedRange: "10.255.0.0/16"}
	return b.sendWireGuardDM(assignment, profile, peer, true)
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
