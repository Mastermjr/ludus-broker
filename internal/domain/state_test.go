package domain

import "testing"

func TestSlotTransitions(t *testing.T) {
	valid := [][2]SlotState{
		{SlotAvailable, SlotReserved},
		{SlotReserved, SlotPreparing},
		{SlotPreparing, SlotReady},
		{SlotReady, SlotAssigned},
		{SlotAssigned, SlotDraining},
		{SlotDraining, SlotResetting},
		{SlotResetting, SlotVerifying},
		{SlotVerifying, SlotAvailable},
	}
	for _, pair := range valid {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected %s -> %s to be valid: %v", pair[0], pair[1], err)
		}
	}
	if err := ValidateTransition(SlotAvailable, SlotAssigned); err == nil {
		t.Fatal("AVAILABLE -> ASSIGNED must be rejected")
	}
}
