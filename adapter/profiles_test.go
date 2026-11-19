package moonraker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/stretchr/testify/require"
	"pcma.local/moonraker/moonrakersim"
)

func TestConfigurationProfileUsesObservedBoundsAndImplementedCommands(t *testing.T) {
	machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{PCMA: true})
	require.NoError(t, err)
	defer machine.Close()
	report, err := ReadConfigurationProfile(t.Context(), machine.Address(), "Klipper", true)
	require.NoError(t, err)
	require.Equal(t, "0.12.0-284-gfb1b4e39", report.Firmware)
	require.Len(t, report.ConfigSHA256, 64)
	var registry struct {
		Schema   int                `json:"schema"`
		Profiles []generatedProfile `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(report.Registry, &registry))
	require.Equal(t, 1, registry.Schema)
	require.Len(t, registry.Profiles, 1)
	profile := registry.Profiles[0]
	require.Equal(t, "Klipper", profile.Model)
	require.Equal(t, "0.12.1-0", profile.Firmware.To)
	require.Equal(t, 300.0, profile.Capabilities[ArgNozzleTarget].Limit)
	require.Equal(t, 120.0, profile.Capabilities[ArgBedTarget].Limit)
	require.True(t, profile.Capabilities.Has(connectorapi.VerbFileUpload))
	require.True(t, profile.Capabilities.Has(VerbStartPrint))
	require.False(t, profile.Capabilities.Has("core.command.move"))
	require.False(t, profile.Capabilities.Has("core.command.home"))
	require.Empty(t, profile.Capabilities.Problems())
}

func TestProfileJSONIsOptionalBoundedAndCopied(t *testing.T) {
	data, err := loadCapabilityProfiles("")
	require.NoError(t, err)
	require.Nil(t, data)
	path := filepath.Join(t.TempDir(), "profile.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema":1,"profiles":[]}`), 0600))
	driver, err := New(connectorapi.Spec{Config: json.RawMessage(`{"capability_profile_path":` + quoted(path) + `}`)})
	require.NoError(t, err)
	first, err := driver.(*Driver).CapabilityProfilesJSON()
	require.NoError(t, err)
	first[0] = '!'
	second, err := driver.(*Driver).CapabilityProfilesJSON()
	require.NoError(t, err)
	require.True(t, json.Valid(second))
	require.NoError(t, os.WriteFile(path, make([]byte, maximumProfileBytes+1), 0600))
	_, err = loadCapabilityProfiles(path)
	require.Error(t, err)
}

func quoted(value string) string { data, _ := json.Marshal(value); return string(data) }

func TestConfigurationProfileRejectsUnusableFirmware(t *testing.T) {
	for _, firmware := range []string{"v0.13.0-foo..bar", "v99999999999999999999999.13.0", "v0.13.2147483647"} {
		t.Run(firmware, func(t *testing.T) {
			machine, err := moonrakersim.Start(t.Context(), moonrakersim.Options{SoftwareVersion: firmware})
			require.NoError(t, err)
			defer machine.Close()
			_, err = ReadConfigurationProfile(t.Context(), machine.Address(), "Klipper", false)
			require.Error(t, err)
		})
	}
}
