package main

import (
	"strings"
	"testing"

	"github.com/Mastermjr/ludus-viberange-broker/internal/domain"
)

func TestAuditEventViewsResolveOnlyDiscordActors(t *testing.T) {
	events := []domain.AuditEvent{
		{ID: 1, Actor: "315696026679771138", Action: "join"},
		{ID: 2, Actor: "broker", Action: "wireguard_issue"},
		{ID: 3, Actor: "315696026679771138", Action: "leave"},
		{ID: 4, Actor: "1404501161339256892", Action: "join"},
	}
	ids := discordAuditActorIDs(events)
	if len(ids) != 2 || ids[0] != "315696026679771138" || ids[1] != "1404501161339256892" {
		t.Fatalf("ids=%v", ids)
	}
	views := auditEventViews(events, map[string]string{"315696026679771138": "Alice\tAdmin\n"})
	if views[0].ActorDisplayName != "Alice Admin" {
		t.Fatalf("sanitized name=%q", views[0].ActorDisplayName)
	}
	if views[1].ActorDisplayName != "-" {
		t.Fatalf("broker display=%q", views[1].ActorDisplayName)
	}
	if views[2].ActorDisplayName != "Alice Admin" {
		t.Fatalf("repeated actor display=%q", views[2].ActorDisplayName)
	}
	if views[3].ActorDisplayName != "<unresolved>" {
		t.Fatalf("unresolved display=%q", views[3].ActorDisplayName)
	}
}

func TestSanitizedDisplayNameIsBounded(t *testing.T) {
	value := sanitizedDisplayName(strings.Repeat("x", 100))
	if len([]rune(value)) != 80 || !strings.HasSuffix(value, "...") {
		t.Fatalf("bounded value length=%d suffix=%q", len([]rune(value)), value[len(value)-3:])
	}
}

func TestDiscordSnowflakeValidation(t *testing.T) {
	for _, value := range []string{"broker", "123", "1234567890123456x", "18446744073709551616"} {
		if isDiscordSnowflake(value) {
			t.Errorf("accepted invalid snowflake %q", value)
		}
	}
	if !isDiscordSnowflake("315696026679771138") {
		t.Fatal("rejected valid Discord snowflake")
	}
}
