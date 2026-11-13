package main

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

type auditView struct {
	domain.AuditEvent
	ActorDisplayName string `json:"actor_display_name"`
}

func discordAuditActorIDs(events []domain.AuditEvent) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, event := range events {
		if !isDiscordSnowflake(event.Actor) {
			continue
		}
		if _, exists := seen[event.Actor]; exists {
			continue
		}
		seen[event.Actor] = struct{}{}
		ids = append(ids, event.Actor)
	}
	return ids
}

func auditEventViews(events []domain.AuditEvent, names map[string]string) []auditView {
	result := make([]auditView, 0, len(events))
	for _, event := range events {
		name := "-"
		if isDiscordSnowflake(event.Actor) {
			name = sanitizedDisplayName(names[event.Actor])
			if name == "" {
				name = "<unresolved>"
			}
		}
		result = append(result, auditView{AuditEvent: event, ActorDisplayName: name})
	}
	return result
}

func isDiscordSnowflake(value string) bool {
	if len(value) < 17 || len(value) > 20 {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func sanitizedDisplayName(value string) string {
	value = strings.TrimSpace(strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value))
	characters := []rune(value)
	if len(characters) > 80 {
		value = string(characters[:77]) + "..."
	}
	return value
}
