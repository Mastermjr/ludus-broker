package domain

import "fmt"

type SlotState string

const (
	SlotAvailable   SlotState = "AVAILABLE"
	SlotReserved    SlotState = "RESERVED"
	SlotPreparing   SlotState = "PREPARING"
	SlotReady       SlotState = "READY"
	SlotAssigned    SlotState = "ASSIGNED"
	SlotDraining    SlotState = "DRAINING"
	SlotResetting   SlotState = "RESETTING"
	SlotVerifying   SlotState = "VERIFYING"
	SlotQuarantined SlotState = "QUARANTINED"
)

var transitions = map[SlotState]map[SlotState]bool{
	SlotAvailable:   {SlotReserved: true, SlotQuarantined: true},
	SlotReserved:    {SlotPreparing: true, SlotAvailable: true, SlotQuarantined: true},
	SlotPreparing:   {SlotReady: true, SlotQuarantined: true},
	SlotReady:       {SlotAssigned: true, SlotResetting: true, SlotQuarantined: true},
	SlotAssigned:    {SlotDraining: true, SlotQuarantined: true},
	SlotDraining:    {SlotResetting: true, SlotQuarantined: true},
	SlotResetting:   {SlotVerifying: true, SlotQuarantined: true},
	SlotVerifying:   {SlotAvailable: true, SlotQuarantined: true},
	SlotQuarantined: {SlotResetting: true},
}

func ValidateTransition(from, to SlotState) error {
	if transitions[from][to] {
		return nil
	}
	return fmt.Errorf("invalid slot transition %s -> %s", from, to)
}
