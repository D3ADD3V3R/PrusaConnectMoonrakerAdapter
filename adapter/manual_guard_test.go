package moonraker

import (
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/stretchr/testify/require"
	"pcma.local/moonraker/moonrakersim"
)

func TestConfirmedFilamentMetadataIsReportedAndUnknownClearsIt(t *testing.T) {
	r := &reader{}
	r.heads.count = 1
	patches := r.patchesFrom(reading(`{"gcode_macro PCMA_STATE":{"contract_version":1,"loaded_material":"PETG","loaded_color":"C_FF8000"}}`, t), time.Now())
	values := map[string]string{}
	for _, patch := range patches {
		values[patch.Path] = string(patch.Value)
	}
	require.Equal(t, `"PETG"`, values[connectorapi.ToolPath("0", connectorapi.MaterialAbbreviation)])
	require.Equal(t, `"PETG"`, values[connectorapi.ToolPath("0", connectorapi.MaterialType)])
	require.Equal(t, `"PETG"`, values[connectorapi.ToolPath("0", connectorapi.MaterialType)])
	require.Equal(t, `"#FF8000"`, values[connectorapi.ToolPath("0", connectorapi.MaterialColor)])
	patches = r.patchesFrom(reading(`{"gcode_macro PCMA_STATE":{"loaded_material":"UNKNOWN","loaded_color":"UNKNOWN"}}`, t), time.Now())
	for _, patch := range patches {
		if patch.Path == connectorapi.ToolPath("0", connectorapi.MaterialAbbreviation) || patch.Path == connectorapi.ToolPath("0", connectorapi.MaterialType) || patch.Path == connectorapi.ToolPath("0", connectorapi.MaterialColor) {
			require.Equal(t, `null`, string(patch.Value))
		}
	}
}

func TestManualOnlyContractReportsMaterialsWithoutEnablingPrintStarts(t *testing.T) {
	r := &reader{}
	r.heads.count = 1
	patches := r.patchesFrom(reading(`{"gcode_macro PCMA_STATE":{"contract_version":0,"filament_contract_version":1,"loaded_material":"PLA","loaded_color":"C_ABCDEF"}}`, t), time.Now())
	values := map[string]string{}
	for _, patch := range patches {
		values[patch.Path] = string(patch.Value)
	}
	require.Equal(t, `"PLA"`, values[connectorapi.ToolPath("0", connectorapi.MaterialType)])
	require.Equal(t, `"#ABCDEF"`, values[connectorapi.ToolPath("0", connectorapi.MaterialColor)])
}

func TestManualOnlyResumeRequiresCompletedFilamentWorkflow(t *testing.T) {
	for _, phase := range []string{"AWAITING_INSERTION", "LOADING", "RECOVERY_REQUIRED", "UNLOADED", "CONFIRMED"} {
		t.Run(phase, func(t *testing.T) {
			machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{PCMA: true})
			require.NoError(t, err)
			defer machine.Close()
			machine.Push(map[string]map[string]any{evoObject: {"contract_version": 0, "filament_contract_version": 1, "phase": "IDLE", "filament_phase": phase, "change_id": "r1", "confirmed_change_id": "r1"}, "pause_resume": {"is_paused": true}, "print_stats": {"state": "paused"}, "webhooks": {"state": "ready"}})
			driver := NewDriver(0)
			session, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address()}, driver.Baseline())
			require.NoError(t, err)
			defer session.Close()
			result, err := session.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint})
			require.NoError(t, err)
			require.Equal(t, phase == "CONFIRMED", result.OK)
			require.Equal(t, phase == "CONFIRMED", machine.Called("printer.print.resume"))
			require.False(t, session.(*Session).caps.Has(VerbStartPrint))
		})
	}
}

func TestResumeCannotOvertakeManualAction(t *testing.T) {
	machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{})
	require.NoError(t, err)
	defer machine.Close()
	driver := NewDriver(0)
	connected, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address()}, driver.Baseline())
	require.NoError(t, err)
	defer connected.Close()
	session := connected.(*Session)
	session.manualActions.Lock()
	result, err := session.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint})
	session.manualActions.Unlock()
	require.NoError(t, err)
	require.False(t, result.OK)
	require.False(t, machine.Called("printer.print.resume"))
}

func TestAbsentFutureMacroIsNotReportedAsSuccessful(t *testing.T) {
	status := moonrakersim.DefaultStatus()
	status["webhooks"] = map[string]any{"state": "ready"}
	status["idle_timeout"] = map[string]any{"state": "Ready"}
	status["pause_resume"] = map[string]any{"is_paused": false}
	machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{Status: status, Objects: append(moonrakersim.DefaultObjects(), "idle_timeout", "gcode_macro LOAD_FILAMENT")})
	require.NoError(t, err)
	defer machine.Close()
	driver := NewDriver(0)
	session, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address()}, driver.Baseline())
	require.NoError(t, err)
	defer session.Close()
	result, err := session.Do(t.Context(), connectorapi.Command{Verb: VerbLoadFilament, Args: map[string]string{"mode": "change", "filament": "PLA", "color": "WHITE"}})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Contains(t, result.MachineReason, "not installed")
	require.Equal(t, []string{"PCMA_FILAMENT_CHANGE MATERIAL=PLA COLOR=WHITE"}, machine.Scripts())
}

func TestResumeWaitsForManualFilamentConfirmationWithoutAStartJournal(t *testing.T) {
	machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{PCMA: true})
	require.NoError(t, err)
	defer machine.Close()
	machine.Push(map[string]map[string]any{evoObject: {"phase": "PAUSED_OTHER", "change_id": "request-1", "confirmed_change_id": "", "filament_phase": "AWAITING_CONFIRMATION"}, "pause_resume": {"is_paused": true}, "print_stats": {"state": "paused"}, "webhooks": {"state": "ready"}})
	driver := NewDriver(0)
	session, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address()}, driver.Baseline())
	require.NoError(t, err)
	defer session.Close()
	result, err := session.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.False(t, machine.Called("printer.print.resume"))
	machine.Push(map[string]map[string]any{evoObject: {"confirmed_change_id": "request-1", "filament_phase": "CONFIRMED"}})
	result, err = session.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint})
	require.NoError(t, err)
	require.True(t, result.OK)
	require.True(t, machine.Called("printer.print.resume"))
}

func TestFilamentRequestIDIsBoundedAndPassedWithoutInjection(t *testing.T) {
	command := connectorapi.Command{Verb: VerbLoadFilament, Args: map[string]string{"mode": "change", "filament": "PETG", "color": "#ff8000", "request_id": "request-1"}}
	script, err := filamentScript(command)
	require.NoError(t, err)
	require.Equal(t, "PCMA_FILAMENT_CHANGE MATERIAL=PETG COLOR=C_FF8000 REQUEST=request-1", script)
	command.Args["request_id"] = "request-1\nRESUME"
	_, err = filamentScript(command)
	require.Error(t, err)
}
