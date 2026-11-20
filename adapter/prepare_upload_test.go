package moonraker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/stretchr/testify/require"
	"pcma.local/moonraker/gcode"
)

func TestAutomaticPreparationFillsOnlyMissingFields(t *testing.T) {
	for _, test := range []struct{ name, source, material, color, phase string }{
		{"both missing", "G90\n", "PLA", "C_112233", "CONFIRMED"},
		{"material missing", "; filament_colour = #ABCDEF\nG90\n", "PLA", "C_ABCDEF", "CONFIRMED"},
		{"color missing", "; filament_type = PETG\nG90\n", "PETG", "C_112233", "CONFIRMED"},
		{"complete source", "; filament_type = PETG\n; filament_colour = #ABCDEF\nG90\n", "PETG", "C_ABCDEF", "RECOVERY_REQUIRED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			machine := startMachineWithMacros(t, false)
			driver := startDriver(t, filepath.Join(t.TempDir(), "journal.db"))
			session := startSession(t, driver, machine)
			machine.Push(map[string]map[string]any{evoObject: {"filament_phase": test.phase, "change_id": "loaded-1", "confirmed_change_id": "loaded-1", "loaded_material": "PLA", "loaded_color": "C_112233"}})
			up := upload(test.source)
			up.Path = "ordinary.gcode"
			prepared, cleanup, err := session.PrepareUpload(t.Context(), up)
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			defer func() { require.NoError(t, cleanup.Close()) }()
			reader, err := prepared.Content.Open(t.Context(), 0)
			require.NoError(t, err)
			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			artifact, err := gcode.Inspect(bytes.NewReader(data))
			require.NoError(t, err)
			require.Equal(t, test.material, artifact.Material)
			require.Equal(t, test.color, artifact.Color)
			require.Equal(t, "gcodes/pcma/"+artifact.ExecutionID+".gcode", prepared.Path)
			require.Equal(t, int64(len(data)), prepared.Content.Size())
			require.Contains(t, string(data), test.source)
			require.False(t, machine.Called("printer.print.start"))
		})
	}
}

func TestPreparationRefusesUnconfirmedFallbackAndReleasesLock(t *testing.T) {
	machine := startMachineWithMacros(t, false)
	session := startSession(t, startDriver(t, filepath.Join(t.TempDir(), "journal.db")), machine)
	_, cleanup, err := session.PrepareUpload(t.Context(), upload("G90\n"))
	require.Error(t, err)
	require.Nil(t, cleanup)
	require.True(t, fault.HasCode(err, connectorapi.TransferRefused.Code()))
	full := upload("; filament_type = PLA\n; filament_colour = #112233\nG90\n")
	_, cleanup, err = session.PrepareUpload(t.Context(), full)
	require.NoError(t, err)
	_, _, err = session.PrepareUpload(t.Context(), full)
	require.ErrorIs(t, err, connectorapi.ErrTransferBusy)
	require.NoError(t, cleanup.Close())
	_, cleanup, err = session.PrepareUpload(t.Context(), full)
	require.NoError(t, err)
	require.NoError(t, cleanup.Close())
}

func TestPreparedIdentityCanRetryAfterUnsentCleanupButNotAfterVerification(t *testing.T) {
	machine := startMachineWithMacros(t, false)
	driver := startDriver(t, filepath.Join(t.TempDir(), "journal.db"))
	session := startSession(t, driver, machine)
	var data bytes.Buffer
	_, err := gcode.Prepare(bytes.NewReader([]byte("G90\n; filament_type = PLA\n")), &data, gcode.Options{ExecutionID: "retry-test", ProfileID: "ender3", ProfileVersion: "v1", Color: "C_112233"})
	require.NoError(t, err)
	up := upload(data.String())
	up.Path = "prepared.gcode"
	_, cleanup, err := session.PrepareUpload(t.Context(), up)
	require.NoError(t, err)
	require.NoError(t, cleanup.Close())
	prepared, cleanup, err := session.PrepareUpload(t.Context(), up)
	require.NoError(t, err)
	require.NoError(t, session.Send(t.Context(), prepared))
	require.NoError(t, cleanup.Close())
	_, _, err = session.PrepareUpload(t.Context(), up)
	require.Error(t, err)
	artifact, err := driver.executions.store.artifact(t.Context(), prepared.Path)
	require.NoError(t, err)
	require.Equal(t, "retry-test", artifact.ExecutionID)
	require.False(t, machine.Called("printer.print.start"))
}

func TestPreparationCancellationAndInvalidMetadataDoNotPoisonNextUpload(t *testing.T) {
	machine := startMachineWithMacros(t, false)
	session := startSession(t, startDriver(t, filepath.Join(t.TempDir(), "journal.db")), machine)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := session.PrepareUpload(ctx, upload("G90\n"))
	require.True(t, errors.Is(err, context.Canceled))
	_, _, err = session.PrepareUpload(t.Context(), upload("; filament_type = PLA;PETG\nG90\n"))
	require.Error(t, err)
	_, cleanup, err := session.PrepareUpload(t.Context(), upload("; filament_type = PLA\n; filament_colour = #112233\nG90\n"))
	require.NoError(t, err)
	require.NoError(t, cleanup.Close())
}
