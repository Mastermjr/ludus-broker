package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/config"
	"github.com/Mastermjr/ludus-viberange-broker/internal/discord"
	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
	"github.com/Mastermjr/ludus-viberange-broker/internal/store"
	"github.com/Mastermjr/ludus-viberange-broker/internal/worker"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	if command == "help" || command == "--help" || command == "-h" {
		usage()
		return nil
	}
	if command == "version" {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if command == "serve" {
		return serve(cfg)
	}
	if command == "discord" {
		return discordCommand(cfg, args)
	}
	if command == "ludus" {
		return ludusCommand(cfg, args)
	}

	database, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("database initialization failed: %w", err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.SeedSlots(ctx, cfg.SlotIDs); err != nil {
		return fmt.Errorf("slot initialization failed: %w", err)
	}
	jsonOutput := contains(args, "--json")

	switch command {
	case "slots":
		values, err := database.ListSlots(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "SLOT\tSTATE\tGENERATION\tLEASE OWNER\tLEASE END")
		for _, value := range values {
			end := "-"
			if value.LeaseEnd != nil {
				end = value.LeaseEnd.UTC().Format("2006-01-02 15:04:05Z")
			}
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n", value.ID, value.State, value.Generation, dash(value.LeaseOwner), end)
		}
		return writer.Flush()
	case "queue":
		values, err := database.ListReservations(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "POSITION\tDISCORD USER\tSTATE\tSLOT\tREQUESTED")
		for _, value := range values {
			position := "-"
			if value.QueuePosition > 0 {
				position = fmt.Sprint(value.QueuePosition)
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", position, value.DiscordUserID, value.State, dash(value.AssignedSlot), value.RequestedAt.UTC().Format("2006-01-02 15:04:05Z"))
		}
		return writer.Flush()
	case "participants":
		reservations, err := database.ListReservations(ctx)
		if err != nil {
			return err
		}
		slots, err := database.ListSlots(ctx)
		if err != nil {
			return err
		}
		jobs, err := database.ListJobs(ctx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(reservations))
		for _, reservation := range reservations {
			ids = append(ids, reservation.DiscordUserID)
		}
		names := map[string]string{}
		if len(ids) > 0 {
			names, err = discord.ResolveDisplayNames(cfg.DiscordTokenFile, cfg.DiscordGuildID, ids)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: Discord names could not be fully resolved: %v\n", err)
			}
		}
		values := participantHealthList(reservations, slots, jobs, names, time.Now().UTC(), cfg.PreparationLead, cfg.NoShowGrace)
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tDISCORD ID\tRESERVATION\tSLOT\tSLOT STATE\tHEALTH\tDETAIL")
		for _, value := range values {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", value.DisplayName, value.DiscordUserID,
				value.ReservationState, dash(value.SlotID), dash(value.SlotState), value.Health, value.Detail)
		}
		return writer.Flush()
	case "cooldowns":
		cooldowns, err := database.ListActiveCooldowns(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(cooldowns))
		for _, cooldown := range cooldowns {
			ids = append(ids, cooldown.DiscordUserID)
		}
		names := map[string]string{}
		if len(ids) > 0 {
			names, err = discord.ResolveDisplayNames(cfg.DiscordTokenFile, cfg.DiscordGuildID, ids)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: Discord names could not be fully resolved: %v\n", err)
			}
		}
		values := make([]cooldownView, 0, len(cooldowns))
		for _, cooldown := range cooldowns {
			name := names[cooldown.DiscordUserID]
			if name == "" {
				name = "<unresolved>"
			}
			values = append(values, cooldownView{DisplayName: name, DiscordUserID: cooldown.DiscordUserID, Until: cooldown.Until, Reason: cooldown.Reason})
		}
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tDISCORD ID\tELIGIBLE AT\tREASON")
		for _, value := range values {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", value.DisplayName, value.DiscordUserID, value.Until.UTC().Format(time.RFC3339), value.Reason)
		}
		return writer.Flush()
	case "participant":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New("usage: viberange-broker participant <discord-user-id> [--json]")
		}
		reservation, err := database.ActiveReservation(ctx, args[0])
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(reservation)
		}
		fmt.Printf("Discord user: %s\nState: %s\nSlot: %s\n", reservation.DiscordUserID, reservation.State, dash(reservation.AssignedSlot))
		return nil
	case "audit":
		events, err := database.ListAuditEvents(ctx, 100)
		if err != nil {
			return err
		}
		names := map[string]string{}
		ids := discordAuditActorIDs(events)
		if len(ids) > 0 {
			names, err = discord.ResolveDisplayNames(cfg.DiscordTokenFile, cfg.DiscordGuildID, ids)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: Discord audit names could not be fully resolved: %v\n", err)
			}
		}
		values := auditEventViews(events, names)
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "TIME\tACTOR ID\tUSERNAME\tACTION\tRESULT\tSLOT")
		for _, event := range values {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", event.OccurredAt.UTC().Format(time.RFC3339), event.Actor, event.ActorDisplayName, event.Action, event.Result, dash(event.SlotID))
		}
		return writer.Flush()
	case "thaw", "clear-cooldown":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker thaw <discord-user-id> --yes")
		}
		if err := database.ClearCooldown(ctx, args[0]); err != nil {
			return err
		}
		fmt.Printf("cleared cooldown for %s; participant may use /range join now\n", args[0])
		return nil
	case "assign":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New("usage: viberange-broker assign <discord-user-id> [--slot user3]")
		}
		preferred, _ := flagValue(args, "--slot")
		id, err := database.EnqueuePreferred(ctx, args[0], preferred)
		if err != nil {
			return err
		}
		fmt.Printf("queued reservation %d for %s", id, args[0])
		if preferred != "" {
			fmt.Printf(" preferring %s", preferred)
		}
		fmt.Println()
		return nil
	case "extend":
		if len(args) < 2 {
			return errors.New("usage: viberange-broker extend <discord-user-id> <duration> --yes")
		}
		if !contains(args, "--yes") {
			return errors.New("lease extension requires --yes")
		}
		extension, err := time.ParseDuration(args[1])
		if err != nil {
			return fmt.Errorf("invalid extension duration: %w", err)
		}
		end, err := database.ExtendLease(ctx, args[0], extension)
		if err != nil {
			return err
		}
		fmt.Printf("extended %s until %s\n", args[0], end.UTC().Format(time.RFC3339))
		return nil
	case "release":
		if len(args) == 0 || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker release <discord-user-id> --yes")
		}
		released, err := database.RequestRelease(ctx, args[0])
		if err != nil {
			return err
		}
		if !released {
			return store.ErrNotFound
		}
		fmt.Println("queued generation-bound revoke/reset release")
		return nil
	case "retry-job":
		if len(args) == 0 || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker retry-job <job-id> --yes")
		}
		jobID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || jobID <= 0 {
			return errors.New("job-id must be a positive integer")
		}
		if err := database.RetryFailedJob(ctx, jobID); err != nil {
			return err
		}
		fmt.Printf("requeued failed job %d\n", jobID)
		return nil
	case "jobs":
		values, err := database.ListJobs(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(values)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "ID\tTYPE\tSLOT\tSTATE\tATTEMPTS\tERROR")
		for _, value := range values {
			fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%d\t%s\n", value.ID, value.Transition, value.SlotID, value.State, value.Attempts, dash(value.LastError))
		}
		return writer.Flush()
	case "reconcile-wireguard":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker reconcile-wireguard <slot> --yes")
		}
		if cfg.LudusURL == "" {
			return errors.New("VBR_LUDUS_URL is required")
		}
		client, err := ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if err != nil {
			return err
		}
		peer, err := client.WireGuardStatus(ctx, args[0])
		if err != nil {
			return err
		}
		if !peer.Revoked || peer.Generation <= 0 {
			return errors.New("refusing generation reconciliation unless plugin reports a revoked positive generation")
		}
		target := peer.Generation - 1
		if err := database.ReconcileAvailableGeneration(ctx, args[0], target); err != nil {
			return err
		}
		fmt.Printf("reconciled %s broker generation to %d for next WireGuard generation %d\n", args[0], target, peer.Generation)
		return nil
	case "restore-slot":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker restore-slot <slot> --yes")
		}
		if cfg.LudusURL == "" {
			return errors.New("VBR_LUDUS_URL is required")
		}
		client, err := ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if err != nil {
			return err
		}
		if _, err := client.ValidateBlueprint(ctx, args[0]); err != nil {
			return err
		}
		peer, err := client.WireGuardStatus(ctx, args[0])
		if err != nil {
			return err
		}
		if peer.Generation <= 0 || peer.PeerID == "" || peer.IssuedAt.IsZero() {
			return errors.New("refusing slot restore because plugin peer metadata is incomplete")
		}
		jobID, err := database.CreateRestoreSlotJob(ctx, args[0], peer.Generation, peer.PeerID, peer.IssuedAt, peer.RevokedAt)
		if err != nil {
			return err
		}
		fmt.Printf("created authoritative RESET job %d for %s at WireGuard generation %d\n", jobID, args[0], peer.Generation)
		return nil
	case "recover-wireguard-drift":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || !contains(args, "--yes") {
			return errors.New("usage: viberange-broker recover-wireguard-drift <slot> --yes")
		}
		if cfg.LudusURL == "" {
			return errors.New("VBR_LUDUS_URL is required")
		}
		client, err := ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if err != nil {
			return err
		}
		if _, err := client.ValidateBlueprint(ctx, args[0]); err != nil {
			return err
		}
		peer, err := client.WireGuardStatus(ctx, args[0])
		if err != nil {
			return err
		}
		if !peer.Revoked || peer.Generation <= 0 || peer.PeerID == "" || peer.IssuedAt.IsZero() || peer.RevokedAt == nil {
			return errors.New("refusing drift recovery unless the plugin reports complete revoked peer metadata")
		}
		jobID, err := database.CreateWireGuardDriftRecoveryJob(ctx, args[0], peer.Generation, peer.PeerID, peer.IssuedAt, *peer.RevokedAt)
		if err != nil {
			return err
		}
		fmt.Printf("adopted revoked WireGuard generation %d for %s and created RESET job %d\n", peer.Generation, args[0], jobID)
		return nil
	case "reset", "quarantine", "recover":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("usage: viberange-broker %s <slot> --yes", command)
		}
		if !contains(args, "--yes") {
			return errors.New("destructive command requires --yes")
		}
		id, created, err := database.CreateSlotJob(ctx, strings.ToUpper(command), args[0])
		if err != nil {
			return err
		}
		if created {
			fmt.Printf("created job %d\n", id)
		} else {
			fmt.Printf("job %d is already pending or completed for this slot generation\n", id)
		}
		return nil
	case "recover-jobs":
		if !contains(args, "--yes") {
			return errors.New("interrupted job recovery requires --yes")
		}
		recovered, err := database.RecoverInterruptedJobs(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("recovered %d interrupted lifecycle jobs\n", recovered)
		return nil
	case "run-job":
		if !contains(args, "--yes") {
			return errors.New("production lifecycle execution requires --yes")
		}
		if cfg.LudusURL == "" {
			return errors.New("VBR_LUDUS_URL is required")
		}
		client, err := ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			Timeout:               30 * time.Minute,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if err != nil {
			return err
		}
		log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "viberange-broker-cli", "version", version)
		if err := worker.New(database, log, cfg.RejoinCooldown).RunProductionOnce(ctx, client); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fmt.Println("no pending lifecycle job")
				return nil
			}
			return err
		}
		fmt.Println("completed one production lifecycle job")
		return nil
	case "doctor":
		checks, checkErr := collectDiagnostics(ctx, cfg, database)
		if err := printChecks(checks, jsonOutput); err != nil {
			return err
		}
		return checkErr
	case "selftest":
		checks, checkErr := collectDiagnostics(ctx, cfg, database)
		if checkErr == nil && cfg.DiscordChannelID != "" {
			checkErr = discord.SelfTestChannel(cfg.DiscordTokenFile, cfg.DiscordChannelID)
			checks["discord_delivery"] = statusValue(checkErr)
		}
		checks["selftest"] = statusValue(checkErr)
		if err := printChecks(checks, jsonOutput); err != nil {
			return err
		}
		return checkErr
	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func serve(cfg config.Config) error {
	if err := cfg.ValidateServe(); err != nil {
		return fmt.Errorf("invalid serve configuration: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "viberange-broker", "version", version)
	database, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("database initialization failed: %w", err)
	}
	defer database.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := database.SeedSlots(ctx, cfg.SlotIDs); err != nil {
		return fmt.Errorf("slot initialization failed: %w", err)
	}
	recovered, err := database.RecoverInterruptedJobs(ctx)
	if err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	recoveredPreparations, err := database.RecoverInterruptedPreparations(ctx)
	if err != nil {
		return fmt.Errorf("recover interrupted preparations: %w", err)
	}
	log.Info("broker initialized", "slots", len(cfg.SlotIDs), "database", cfg.DatabasePath, "mode", cfg.Mode, "recovered_jobs", recovered, "quarantined_preparations", recoveredPreparations)
	var ludusClient ludus.Client
	if cfg.Mode == "simulation" {
		go worker.New(database, log, cfg.RejoinCooldown).RunSimulation(ctx)
	} else {
		ludusClient, err = ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			Timeout:               30 * time.Minute,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if err != nil {
			return fmt.Errorf("Ludus client initialization failed: %w", err)
		}
		capability, hashes, err := validateProductionLudus(ctx, ludusClient, cfg.SlotIDs)
		if err != nil {
			return err
		}
		log.Info("WireGuard plugin ready", "name", capability.Name, "version", capability.Version)
		log.Info("all slot blueprints approved", "slots", len(hashes))
		go worker.New(database, log, cfg.RejoinCooldown, cfg.NoShowGrace).RunProduction(ctx, ludusClient)
	}
	bot, err := discord.New(cfg.DiscordTokenFile, cfg.DiscordGuildID, cfg.DiscordChannelID, cfg.DiscordParticipantRoleID, cfg.Mode, cfg.LeaseDuration, cfg.RejoinCooldown, database, log, ludusClient)
	if err != nil {
		return fmt.Errorf("Discord bot initialization failed: %w", err)
	}
	if err := bot.Run(ctx); err != nil {
		return fmt.Errorf("Discord bot stopped unexpectedly: %w", err)
	}
	log.Info("broker stopping")
	return nil
}

type productionStartupValidator interface {
	WireGuardHealth(context.Context) (ludus.WireGuardCapability, error)
	ValidateBlueprint(context.Context, string) (string, error)
}

func validateProductionLudus(ctx context.Context, client productionStartupValidator, slots []string) (ludus.WireGuardCapability, map[string]string, error) {
	capability, err := client.WireGuardHealth(ctx)
	if err != nil {
		return capability, nil, fmt.Errorf("WireGuard plugin readiness failed: %w", err)
	}
	if !capability.Ready {
		return capability, nil, errors.New("WireGuard plugin reported not ready")
	}
	hashes := make(map[string]string, len(slots))
	for _, slot := range slots {
		hash, err := client.ValidateBlueprint(ctx, slot)
		if err != nil {
			return capability, nil, fmt.Errorf("startup blueprint validation failed for %s: %w", slot, err)
		}
		hashes[slot] = hash
	}
	return capability, hashes, nil
}

func discordCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: viberange-broker discord <discover|configure>")
	}
	if cfg.DiscordGuildID == "" {
		return errors.New("VBR_DISCORD_GUILD_ID is required")
	}
	resources, err := discord.Discover(cfg.DiscordTokenFile, cfg.DiscordGuildID)
	if err != nil {
		return err
	}
	switch args[0] {
	case "discover":
		if contains(args, "--json") {
			return printJSON(resources)
		}
		fmt.Printf("Guild ID: %s\n\nChannels:\n", resources.GuildID)
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tID\tTYPE")
		for _, channel := range resources.Channels {
			fmt.Fprintf(writer, "%s\t%s\t%s\n", channel.Name, channel.ID, channel.Type)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		fmt.Println("\nRoles:")
		writer = tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tID\tMANAGED\tPOSITION")
		for _, role := range resources.Roles {
			fmt.Fprintf(writer, "%s\t%s\t%v\t%d\n", role.Name, role.ID, role.Managed, role.Position)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		fmt.Println("\nSet VBR_DISCORD_CHANNEL_ID and VBR_DISCORD_PARTICIPANT_ROLE_ID to the selected immutable IDs.")
		return nil
	case "configure":
		channelValue, ok := flagValue(args, "--channel")
		if !ok {
			return errors.New("discord configure requires --channel <name-or-id>")
		}
		roleValue, ok := flagValue(args, "--participant-role")
		if !ok {
			return errors.New("discord configure requires --participant-role <name-or-id>")
		}
		channelID, err := resolveChannel(resources, channelValue)
		if err != nil {
			return err
		}
		roleID, err := resolveRole(resources, roleValue)
		if err != nil {
			return err
		}
		fmt.Printf("VBR_DISCORD_GUILD_ID=%s\nVBR_DISCORD_CHANNEL_ID=%s\nVBR_DISCORD_PARTICIPANT_ROLE_ID=%s\n", resources.GuildID, channelID, roleID)
		return nil
	default:
		return errors.New("usage: viberange-broker discord <discover|configure>")
	}
}

func flagValue(args []string, name string) (string, bool) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name && args[index+1] != "" {
			return args[index+1], true
		}
	}
	return "", false
}

func resolveChannel(resources discord.ResourceDiscovery, value string) (string, error) {
	var matches []string
	for _, channel := range resources.Channels {
		if channel.ID == value || channel.Name == value {
			matches = append(matches, channel.ID)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("channel %q matched %d visible text channels", value, len(matches))
	}
	return matches[0], nil
}

func resolveRole(resources discord.ResourceDiscovery, value string) (string, error) {
	var matches []discord.DiscoveredRole
	for _, role := range resources.Roles {
		if role.ID == value || role.Name == value {
			matches = append(matches, role)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("role %q matched %d roles", value, len(matches))
	}
	role := matches[0]
	if role.Managed || role.ID == resources.GuildID || role.Name == "@everyone" {
		return "", fmt.Errorf("role %q is managed or @everyone and cannot authorize participants", value)
	}
	return role.ID, nil
}

func ludusCommand(cfg config.Config, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: viberange-broker ludus <inventory|preflight|reset|wireguard-status|wireguard-issue|wireguard-revoke> <user> [options]")
	}
	if cfg.LudusURL == "" {
		return errors.New("VBR_LUDUS_URL is required")
	}
	timeout := 30 * time.Second
	if args[0] == "reset" {
		timeout = 30 * time.Minute
	}
	client, err := ludus.NewHTTPClient(ludus.HTTPOptions{
		BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
		Timeout:               timeout,
		InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
		TLSCertSHA256:         cfg.LudusTLSCertSHA256,
		BlueprintSHA256:       cfg.LudusBlueprintSHA256,
		BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
	})
	if err != nil {
		return err
	}
	ctx := context.Background()
	userID := args[1]
	switch args[0] {
	case "inventory":
		inventory, err := client.Inventory(ctx, userID)
		if err != nil {
			return err
		}
		if contains(args, "--json") {
			return printJSON(inventory)
		}
		fmt.Printf("Range: %s\nState: %s\nTesting: %v\n\n", inventory.ID, inventory.State, inventory.Testing)
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "VM\tIP\tPOWERED ON")
		for _, vm := range inventory.VMs {
			fmt.Fprintf(writer, "%s\t%s\t%v\n", vm.Name, vm.IP, vm.PoweredOn)
		}
		return writer.Flush()
	case "preflight":
		blueprintHash, err := client.ValidateBlueprint(ctx, userID)
		if err != nil {
			return err
		}
		inventory, err := client.Inventory(ctx, userID)
		if err != nil {
			return err
		}
		if !inventory.Testing {
			return errors.New("range is not in Ludus testing state")
		}
		readiness, err := client.VerifyRange(ctx, userID)
		if err != nil {
			return err
		}
		if !readiness.Ready {
			return fmt.Errorf("range is not ready: %s", strings.Join(readiness.Failures, "; "))
		}
		result := map[string]any{
			"user_id": userID, "blueprint_sha256": blueprintHash,
			"inventory": inventory, "readiness": readiness,
		}
		if contains(args, "--json") {
			return printJSON(result)
		}
		fmt.Printf("Preflight passed for %s\nBlueprint SHA-256: %s\nRange: %s\nTesting: %v\nVMs: %d\n",
			userID, blueprintHash, inventory.State, inventory.Testing, len(inventory.VMs))
		return nil
	case "wireguard-status":
		peer, err := client.WireGuardStatus(ctx, userID)
		if err != nil {
			return err
		}
		return printJSON(peer)
	case "wireguard-issue":
		if !contains(args, "--yes") {
			return errors.New("WireGuard peer issuance requires --yes")
		}
		generation, err := generationFlag(args)
		if err != nil {
			return err
		}
		output, ok := flagValue(args, "--output")
		if !ok {
			return errors.New("wireguard-issue requires --output <protected-conf-file>")
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create protected profile output: %w", err)
		}
		keep := false
		defer func() {
			_ = file.Close()
			if !keep {
				_ = os.Remove(output)
			}
		}()
		access, err := client.IssueWireGuard(ctx, userID, generation)
		if err != nil {
			return err
		}
		defer access.Destroy()
		profile, err := access.ConsumeProfile()
		if err != nil {
			return err
		}
		defer wipe(profile)
		if _, err := file.Write(profile); err != nil {
			return fmt.Errorf("write protected profile output: %w", err)
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync protected profile output: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close protected profile output: %w", err)
		}
		keep = true
		fmt.Printf("Issued WireGuard generation %d for %s into protected file %s\n", generation, userID, output)
		return nil
	case "wireguard-revoke":
		if !contains(args, "--yes") {
			return errors.New("WireGuard peer revocation requires --yes")
		}
		generation, err := generationFlag(args)
		if err != nil {
			return err
		}
		peer, err := client.RevokeWireGuard(ctx, userID, generation)
		if err != nil {
			return err
		}
		return printJSON(peer)
	case "reset":
		if !contains(args, "--yes") {
			return errors.New("destructive Ludus reset requires --yes")
		}
		if err := client.ResetRange(ctx, userID); err != nil {
			return err
		}
		readiness, err := client.VerifyRange(ctx, userID)
		if err != nil {
			return err
		}
		if !readiness.Ready {
			return fmt.Errorf("post-reset range is not ready: %s", strings.Join(readiness.Failures, "; "))
		}
		if contains(args, "--json") {
			return printJSON(map[string]any{"user_id": userID, "reset": "succeeded", "readiness": readiness})
		}
		fmt.Printf("Reset and readiness validation succeeded for %s\n", userID)
		return nil
	default:
		return errors.New("usage: viberange-broker ludus <inventory|preflight|reset|wireguard-status|wireguard-issue|wireguard-revoke> <user> [options]")
	}
}

func generationFlag(args []string) (int64, error) {
	value, ok := flagValue(args, "--generation")
	if !ok {
		return 0, errors.New("--generation <positive-integer> is required")
	}
	generation, err := strconv.ParseInt(value, 10, 64)
	if err != nil || generation <= 0 {
		return 0, errors.New("--generation must be a positive integer")
	}
	return generation, nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func collectDiagnostics(ctx context.Context, cfg config.Config, database *store.Store) (map[string]any, error) {
	count, err := database.SlotCount(ctx)
	if err != nil {
		return map[string]any{"database": "failed"}, err
	}
	checks := map[string]any{
		"database":                    "ok",
		"slots":                       count,
		"mode":                        cfg.Mode,
		"discord_guild_configured":    cfg.DiscordGuildID != "",
		"discord_channel_configured":  cfg.DiscordChannelID != "",
		"participant_role_configured": cfg.DiscordParticipantRoleID != "",
		"discord_api":                 "not_checked",
	}
	if cfg.DiscordGuildID != "" {
		skew, clockErr := discord.CheckClock(ctx, 2*time.Minute)
		if clockErr != nil {
			checks["clock"] = "failed: " + clockErr.Error()
			return checks, clockErr
		}
		checks["clock"] = "ok"
		checks["discord_clock_skew"] = skew.Round(time.Second).String()
		resources, discoverErr := discord.Discover(cfg.DiscordTokenFile, cfg.DiscordGuildID)
		if discoverErr != nil {
			checks["discord_api"] = "failed: " + discoverErr.Error()
			return checks, fmt.Errorf("Discord diagnostic failed: %w", discoverErr)
		}
		checks["discord_api"] = "ok"
		channelFound := false
		for _, channel := range resources.Channels {
			channelFound = channelFound || channel.ID == cfg.DiscordChannelID
		}
		checks["discord_channel_exists"] = channelFound
		roleFound := cfg.DiscordParticipantRoleID == "" && cfg.Mode == "simulation"
		for _, role := range resources.Roles {
			if role.ID == cfg.DiscordParticipantRoleID && !role.Managed && role.ID != cfg.DiscordGuildID {
				roleFound = true
			}
		}
		checks["participant_role_valid"] = roleFound
		if !channelFound || !roleFound {
			return checks, errors.New("configured Discord channel or participant role is invalid")
		}
	}
	if cfg.LudusURL != "" {
		client, clientErr := ludus.NewHTTPClient(ludus.HTTPOptions{
			BaseURL: cfg.LudusURL, APIKeyFile: cfg.LudusAPIKeyFile,
			InsecureSkipVerify:    cfg.LudusInsecureSkipVerify,
			TLSCertSHA256:         cfg.LudusTLSCertSHA256,
			BlueprintSHA256:       cfg.LudusBlueprintSHA256,
			BlueprintSHA256ByUser: cfg.LudusBlueprintSHA256BySlot,
		})
		if clientErr != nil {
			checks["ludus_api"] = "failed: " + clientErr.Error()
			return checks, fmt.Errorf("Ludus diagnostic failed: %w", clientErr)
		}
		version, versionErr := client.Version(ctx)
		if versionErr != nil {
			checks["ludus_api"] = "failed: " + versionErr.Error()
			return checks, fmt.Errorf("Ludus diagnostic failed: %w", versionErr)
		}
		checks["ludus_api"] = "ok"
		checks["ludus_server_version"] = version.Server
		if cfg.Mode == "production" {
			capability, pluginErr := client.WireGuardHealth(ctx)
			if pluginErr != nil || !capability.Ready {
				if pluginErr != nil {
					checks["wireguard_plugin"] = statusValue(pluginErr)
				} else {
					checks["wireguard_plugin"] = "failed: plugin not ready"
				}
				if pluginErr != nil {
					return checks, fmt.Errorf("WireGuard plugin diagnostic failed: %w", pluginErr)
				}
				return checks, errors.New("WireGuard plugin diagnostic reported not ready")
			}
			checks["wireguard_plugin"] = "ok"
			checks["wireguard_plugin_version"] = capability.Version
		}
	} else {
		checks["ludus_api"] = "not_configured"
	}
	return checks, nil
}

func printChecks(checks map[string]any, jsonOutput bool) error {
	if jsonOutput {
		return printJSON(checks)
	}
	for _, key := range []string{"database", "slots", "mode", "clock", "discord_clock_skew", "discord_guild_configured", "discord_channel_configured", "participant_role_configured", "discord_api", "discord_channel_exists", "participant_role_valid", "discord_delivery", "ludus_api", "ludus_server_version", "wireguard_plugin", "wireguard_plugin_version", "selftest"} {
		if _, exists := checks[key]; !exists {
			continue
		}
		fmt.Printf("%-30s %v\n", key, checks[key])
	}
	return nil
}

func statusValue(err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	return "ok"
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() {
	fmt.Print(`Usage: viberange-broker <command> [options]

Server:
  serve                         Run the Discord bot and lifecycle server

Inspection:
  slots [--json]                List slot state
  queue [--json]                List active queue entries and leases
  participants [--json]         List active users, Discord names, and health
  cooldowns [--json]            List users currently blocked by cooldown
  participant <id> [--json]     Show one participant's active state
  jobs [--json]                 List lifecycle jobs
  audit [--json]                List recent sanitized audit events
  doctor [--json]               Validate local configuration and database
  selftest [--json]             Run bounded non-destructive checks
  discord discover [--json]     List visible channel and role IDs
  discord configure ...         Resolve channel/role names to immutable IDs
  ludus inventory <user>        Read authoritative range inventory
  ludus preflight <user>        Validate blueprint, testing, and readiness
  ludus reset <user> --yes      Revert and recreate a clean testing baseline
  ludus wireguard-status <user> Show non-secret peer metadata
  ludus wireguard-issue <user>  Issue into an exclusive mode-0600 .conf file
  ludus wireguard-revoke <user> Revoke an exact peer generation

Administrative jobs:
  assign <id> [--slot <slot>]   Queue a participant, optionally for one slot
  thaw <id> --yes               Clear one participant cooldown with audit
  extend <id> <duration> --yes  Extend an active lease
  release <id> --yes            Revoke and reset an active lease
  reconcile-wireguard <slot>    Align an available slot after out-of-band testing
  restore-slot <slot> --yes     Revoke current access and restore a clean available slot
  recover-wireguard-drift <slot> --yes
                                Adopt a proven-revoked newer peer and queue cleanup
  reset <slot> --yes            Request a clean reset
  retry-job <id> --yes          Retry a failed quarantined lifecycle job
  run-job --yes                 Execute one pending production lifecycle job
  recover-jobs --yes            Return interrupted RUNNING jobs to PENDING
  quarantine <slot> --yes       Remove a slot from scheduling
  recover <slot> --yes          Request recovery of a quarantined slot

Other:
  version                       Print version
  help                          Show this help
`)
}
