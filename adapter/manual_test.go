package moonraker_test

import (
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/stretchr/testify/require"
	"pcma.local/moonraker"
	"pcma.local/moonraker/moonrakersim"
)

func manualMachine(t *testing.T, edit func(map[string]map[string]any)) (*moonrakersim.Host, connectorapi.Session) {
	t.Helper()
	status := moonrakersim.DefaultStatus()
	status["webhooks"] = map[string]any{"state": "ready"}
	status["idle_timeout"] = map[string]any{"state": "Ready"}
	status["pause_resume"] = map[string]any{"is_paused": false}
	status["toolhead"]["homed_axes"] = "xyz"
	status["toolhead"]["position"] = []float64{100, 100, 100, 0}
	if edit != nil {
		edit(status)
	}
	objects := append(moonrakersim.DefaultObjects(), "idle_timeout", "probe", "bed_mesh", "gcode_macro G29", "gcode_macro LOAD_FILAMENT", "gcode_macro UNLOAD_FILAMENT", "gcode_macro PCMA_FILAMENT_CHANGE", "gcode_macro PCMA_LOAD_FILAMENT")
	machine := startMachine(t, moonrakersim.Options{Objects: objects, Status: status})
	return machine, connect(t, machine)
}

func TestManualActionsAndFilamentMetadata(t *testing.T) {
	for _, item := range []struct {
		verb   connectorapi.Capability
		args   map[string]string
		script string
	}{
		{moonraker.VerbHome, nil, "G28"},
		{moonraker.VerbMesh, nil, "G28\nG29"},
		{moonraker.VerbLoadFilament, nil, "LOAD_FILAMENT"},
		{moonraker.VerbUnloadFilament, nil, "UNLOAD_FILAMENT"},
		{moonraker.VerbLoadFilament, map[string]string{"mode": "change", "filament": "petg", "color": "#ff8000"}, "PCMA_FILAMENT_CHANGE MATERIAL=PETG COLOR=C_FF8000"},
		{moonraker.VerbLoadFilament, map[string]string{"filament": "PLA", "color": "WHITE"}, "PCMA_LOAD_FILAMENT MATERIAL=PLA COLOR=WHITE"},
		{moonraker.VerbMove, map[string]string{"x": "10", "y": "-2.5", "feedrate": "3000"}, "SAVE_GCODE_STATE NAME=PCMA_MANUAL_JOG\nG91\nG1 X10 Y-2.5 F3000\nM400\nRESTORE_GCODE_STATE NAME=PCMA_MANUAL_JOG"},
		{moonraker.VerbMoveZ, map[string]string{"distance": "-1", "feedrate": "600"}, "SAVE_GCODE_STATE NAME=PCMA_MANUAL_JOG\nG91\nG1 Z-1 F600\nM400\nRESTORE_GCODE_STATE NAME=PCMA_MANUAL_JOG"},
	} {
		t.Run(item.script, func(t *testing.T) {
			machine, session := manualMachine(t, nil)
			result, err := session.Do(t.Context(), connectorapi.Command{Verb: item.verb, Args: item.args})
			require.NoError(t, err)
			require.True(t, result.OK, result.MachineReason)
			require.Equal(t, []string{item.script}, machine.Scripts())
		})
	}
}

func TestManualActionsRefuseUnsafeStateAndArguments(t *testing.T) {
	for _, item := range []struct {
		name string
		edit func(map[string]map[string]any)
		verb connectorapi.Capability
		args map[string]string
	}{
		{"unhomed", func(s map[string]map[string]any) { s["toolhead"]["homed_axes"] = "" }, moonraker.VerbMove, map[string]string{"x": "1", "feedrate": "3000"}},
		{"out of bounds", func(s map[string]map[string]any) { s["toolhead"]["position"] = []float64{100, 100, 50, 0} }, moonraker.VerbMoveZ, map[string]string{"distance": "-100", "feedrate": "600"}},
		{"printing", func(s map[string]map[string]any) { s["print_stats"]["state"] = "printing" }, moonraker.VerbHome, nil},
		{"busy", func(s map[string]map[string]any) { s["idle_timeout"]["state"] = "Printing" }, moonraker.VerbMesh, nil},
		{"missing state", func(s map[string]map[string]any) { delete(s, "webhooks") }, moonraker.VerbHome, nil},
		{"paused movement", func(s map[string]map[string]any) {
			s["print_stats"]["state"] = "paused"
			s["pause_resume"]["is_paused"] = true
		}, moonraker.VerbHome, nil},
		{"no color", nil, moonraker.VerbLoadFilament, map[string]string{"mode": "change", "filament": "PLA"}},
		{"injected color", nil, moonraker.VerbLoadFilament, map[string]string{"mode": "change", "filament": "PLA", "color": "WHITE\nG28"}},
		{"injected material", nil, moonraker.VerbLoadFilament, map[string]string{"mode": "change", "filament": "PLA G28", "color": "WHITE"}},
		{"no axes", nil, moonraker.VerbMove, map[string]string{"feedrate": "3000"}},
		{"nan", nil, moonraker.VerbMove, map[string]string{"x": "NaN", "feedrate": "3000"}},
	} {
		t.Run(item.name, func(t *testing.T) {
			machine, session := manualMachine(t, item.edit)
			result, err := session.Do(t.Context(), connectorapi.Command{Verb: item.verb, Args: item.args})
			require.True(t, err != nil || !result.OK)
			require.Empty(t, machine.Scripts())
		})
	}
}

func TestFilamentInPauseDoesNotResume(t *testing.T) {
	machine, session := manualMachine(t, func(s map[string]map[string]any) {
		s["print_stats"]["state"] = "paused"
		s["pause_resume"]["is_paused"] = true
		s["idle_timeout"]["state"] = "Printing"
	})
	result, err := session.Do(t.Context(), connectorapi.Command{Verb: moonraker.VerbLoadFilament, Args: map[string]string{"mode": "change", "filament": "PLA", "color": "BLACK"}})
	require.NoError(t, err)
	require.True(t, result.OK)
	require.Len(t, machine.Scripts(), 1)
	require.False(t, strings.Contains(machine.Scripts()[0], "RESUME"))
	for _, call := range machine.Calls() {
		require.NotEqual(t, "printer.print.resume", call)
	}
}
