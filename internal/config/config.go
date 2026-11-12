package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabasePath               string
	Mode                       string
	DiscordTokenFile           string
	DiscordGuildID             string
	DiscordChannelID           string
	DiscordParticipantRoleID   string
	LudusURL                   string
	LudusAPIKeyFile            string
	LudusInsecureSkipVerify    bool
	LudusTLSCertSHA256         string
	LudusBlueprintSHA256       string
	LudusBlueprintSHA256BySlot map[string]string
	SlotIDs                    []string
	LeaseDuration              time.Duration
	PreparationLead            time.Duration
	NoShowGrace                time.Duration
	RejoinCooldown             time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		DatabasePath:               env("VBR_DATABASE_PATH", "/var/lib/viberange-broker/state/broker.db"),
		Mode:                       env("VBR_MODE", "simulation"),
		DiscordTokenFile:           env("VBR_DISCORD_TOKEN_FILE", "/run/credentials/viberange-broker.service/discord-token"),
		DiscordGuildID:             os.Getenv("VBR_DISCORD_GUILD_ID"),
		DiscordChannelID:           os.Getenv("VBR_DISCORD_CHANNEL_ID"),
		DiscordParticipantRoleID:   os.Getenv("VBR_DISCORD_PARTICIPANT_ROLE_ID"),
		LudusURL:                   os.Getenv("VBR_LUDUS_URL"),
		LudusAPIKeyFile:            env("VBR_LUDUS_API_KEY_FILE", "/run/credentials/viberange-broker.service/ludus-api-key"),
		LudusInsecureSkipVerify:    env("VBR_LUDUS_INSECURE_SKIP_VERIFY", "false") == "true",
		LudusTLSCertSHA256:         os.Getenv("VBR_LUDUS_TLS_CERT_SHA256"),
		LudusBlueprintSHA256:       os.Getenv("VBR_LUDUS_BLUEPRINT_SHA256"),
		LudusBlueprintSHA256BySlot: splitMap(os.Getenv("VBR_LUDUS_BLUEPRINT_SHA256_BY_SLOT")),
		SlotIDs:                    split(env("VBR_SLOT_IDS", "user1,user2,user3,user4,user5")),
		LeaseDuration:              time.Hour,
		PreparationLead:            15 * time.Minute,
		NoShowGrace:                10 * time.Minute,
		RejoinCooldown:             30 * time.Minute,
	}
	for name, target := range map[string]*time.Duration{
		"VBR_LEASE_DURATION":   &cfg.LeaseDuration,
		"VBR_REJOIN_COOLDOWN":  &cfg.RejoinCooldown,
		"VBR_PREPARATION_LEAD": &cfg.PreparationLead,
		"VBR_NO_SHOW_GRACE":    &cfg.NoShowGrace,
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", name)
			}
			*target = parsed
		}
	}
	if len(cfg.SlotIDs) == 0 {
		return Config{}, fmt.Errorf("VBR_SLOT_IDS must contain at least one slot")
	}
	if cfg.Mode != "simulation" && cfg.Mode != "production" {
		return Config{}, fmt.Errorf("VBR_MODE must be simulation or production")
	}
	return cfg, nil
}

func (c Config) ValidateServe() error {
	if c.DiscordGuildID == "" {
		return fmt.Errorf("VBR_DISCORD_GUILD_ID is required")
	}
	if c.DiscordChannelID == "" {
		return fmt.Errorf("VBR_DISCORD_CHANNEL_ID is required")
	}
	if c.Mode == "production" {
		if c.DiscordParticipantRoleID == "" {
			return fmt.Errorf("VBR_DISCORD_PARTICIPANT_ROLE_ID is required in production mode")
		}
		if c.LudusURL == "" {
			return fmt.Errorf("VBR_LUDUS_URL is required in production mode")
		}
		if c.LudusBlueprintSHA256 == "" {
			if len(c.SlotIDs) == 0 {
				return fmt.Errorf("VBR_LUDUS_BLUEPRINT_SHA256 or VBR_LUDUS_BLUEPRINT_SHA256_BY_SLOT is required in production mode")
			}
			for _, slotID := range c.SlotIDs {
				if c.LudusBlueprintSHA256BySlot[slotID] == "" {
					return fmt.Errorf("VBR_LUDUS_BLUEPRINT_SHA256 or a complete VBR_LUDUS_BLUEPRINT_SHA256_BY_SLOT map is required in production mode")
				}
			}
		}
		if c.LudusTLSCertSHA256 == "" && !c.LudusInsecureSkipVerify {
			return fmt.Errorf("VBR_LUDUS_TLS_CERT_SHA256 is required in production mode")
		}
	}
	return nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func splitMap(value string) map[string]string {
	result := make(map[string]string)
	for _, item := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
