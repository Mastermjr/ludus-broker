package discord

import (
	"testing"
	"time"
)

func TestValidateClockSkew(t *testing.T) {
	now := time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC)
	if _, err := validateClockSkew(now, now.Add(30*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := validateClockSkew(now, now.Add(18*time.Hour), time.Minute); err == nil {
		t.Fatal("large clock skew was accepted")
	}
}
