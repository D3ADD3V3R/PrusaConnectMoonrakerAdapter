package moonraker

// The states this dialect reports at /state, the moves between them, and the
// verbs each one accepts. The words are the values of stateWords in
// telemetry.go. A word Klipper adds that the table does not carry still reaches
// the tree in upper case, and a client then finds no entry for it here.
//
// The guards follow Klipper's own pause_resume section: PAUSE does nothing to a
// print that is already paused, and RESUME does nothing to one that is not.
// Neither is an error on the wire, so a guard is the only place a client can
// learn it. A heater target and a status read are accepted in every state.

import (
	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// The state words, as stateWords writes them.
const (
	stateIdle     connectorapi.StateName = "IDLE"
	statePrinting connectorapi.StateName = "PRINTING"
	statePaused   connectorapi.StateName = "PAUSED"
	stateFinished connectorapi.StateName = "FINISHED"
	stateStopped  connectorapi.StateName = "STOPPED"
	stateError    connectorapi.StateName = "ERROR"
)

// states declares the device states and command transitions. The start handler
// additionally checks the durable reservation and the local bed confirmation.
func states() connectorapi.StateSet {
	idle := []connectorapi.Capability{VerbSendStateInfo, VerbNozzleTarget, VerbBedTarget}
	idle = append(idle, connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo, connectorapi.VerbCreateFolder, connectorapi.VerbDeleteFile, connectorapi.VerbDeleteFolder)

	return connectorapi.StateSet{
		States: []connectorapi.DeviceState{
			{Name: stateIdle, DisplayName: "Idle", Accepts: append([]connectorapi.Capability{VerbStartPrint}, idle...)},
			{Name: statePrinting, DisplayName: "Printing", Accepts: append(
				[]connectorapi.Capability{VerbPausePrint, VerbStopPrint}, idle...)},
			{Name: statePaused, DisplayName: "Paused", Accepts: append(
				[]connectorapi.Capability{VerbResumePrint, VerbStopPrint}, idle...)},
			{Name: stateFinished, DisplayName: "Finished", Accepts: append([]connectorapi.Capability{VerbStartPrint}, idle...)},
			{Name: stateStopped, DisplayName: "Stopped", Accepts: append([]connectorapi.Capability{VerbStartPrint}, idle...)},
			{Name: stateError, DisplayName: "Error", Accepts: append([]connectorapi.Capability{VerbStartPrint}, idle...)},
		},
		Transitions: []connectorapi.Transition{
			{From: stateIdle, To: statePrinting, Verb: VerbStartPrint},
			{From: stateFinished, To: statePrinting, Verb: VerbStartPrint},
			{From: stateStopped, To: statePrinting, Verb: VerbStartPrint},
			{From: stateError, To: statePrinting, Verb: VerbStartPrint},
			{From: statePrinting, To: statePaused, Verb: VerbPausePrint},
			{From: statePaused, To: statePrinting, Verb: VerbResumePrint},
			{From: statePrinting, To: stateStopped, Verb: VerbStopPrint},
			{From: statePaused, To: stateStopped, Verb: VerbStopPrint},
			{From: statePrinting, To: stateFinished},
			{From: statePrinting, To: stateError},
			{From: stateFinished, To: stateIdle},
			{From: stateStopped, To: stateIdle},
			{From: stateError, To: stateIdle},
		},
	}
}
