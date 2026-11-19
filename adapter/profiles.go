package moonraker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

const maximumProfileBytes = 1 << 20

func loadCapabilityProfiles(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumProfileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximumProfileBytes || !json.Valid(data) {
		return nil, fmt.Errorf("profile must be valid JSON no larger than 1 MiB")
	}
	return data, nil
}

// CapabilityProfilesJSON supplies the pinned host's validated profile loader.
func (d *Driver) CapabilityProfilesJSON() ([]byte, error) {
	return bytes.Clone(d.capabilityProfiles), nil
}

var firmwarePattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+][0-9A-Za-z.-]+)?$`)

func normalizedFirmware(reported string) string {
	if validProfileFirmware(reported) {
		return strings.TrimPrefix(reported, "v")
	}
	return reported
}

func validProfileFirmware(reported string) bool {
	parts := firmwarePattern.FindStringSubmatch(reported)
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts[1:] {
		if _, err := strconv.ParseUint(part, 10, 31); err != nil {
			return false
		}
	}
	base, _, _ := strings.Cut(strings.TrimPrefix(reported, "v"), "+")
	if _, tag, tagged := strings.Cut(base, "-"); tagged {
		for _, identifier := range strings.Split(tag, ".") {
			if identifier == "" {
				return false
			}
		}
	}
	return true
}

type generatedProfile struct {
	Model    string `json:"model"`
	Firmware struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"firmware"`
	Capabilities connectorapi.Set `json:"capabilities"`
}

// ConfigurationProfile is a profile registry and the selected configuration evidence.
type ConfigurationProfile struct {
	Registry     json.RawMessage          `json:"registry"`
	Model        string                   `json:"model"`
	Firmware     string                   `json:"firmware"`
	ConfigSHA256 string                   `json:"config_sha256"`
	Objects      []string                 `json:"objects"`
	Heaters      map[string]heaterConfig  `json:"heaters"`
	Axes         map[string]stepperBounds `json:"axes"`
}

// ReadConfigurationProfile subscribes to status and reads configuration without sending printer commands.
// The generated range ends before the next patch release; regenerate after configuration or firmware changes.
func ReadConfigurationProfile(ctx context.Context, address, model string, uploads bool) (ConfigurationProfile, error) {
	driver := newDriver(defaultTimeout, liveness{})
	driver.uploads = uploads
	connected, err := driver.Connect(ctx, connectorapi.Endpoint{Address: address, Options: map[string]string{optionModel: model}}, driver.Baseline())
	if err != nil {
		return ConfigurationProfile{}, err
	}
	defer connected.Close()
	session := connected.(*Session)
	identity := session.Identity()
	if len(extrudersOf(session.objects)) != 1 {
		return ConfigurationProfile{}, fmt.Errorf("profile generation currently requires exactly one extruder")
	}
	parts := firmwarePattern.FindStringSubmatch(identity.Firmware.String())
	if !validProfileFirmware(identity.Firmware.String()) {
		return ConfigurationProfile{}, fmt.Errorf("firmware cannot be bounded by a version profile")
	}
	patch, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return ConfigurationProfile{}, fmt.Errorf("firmware patch version: %w", err)
	}
	if patch >= math.MaxInt32 {
		return ConfigurationProfile{}, fmt.Errorf("firmware patch version has no supported upper bound")
	}
	caps, _ := session.Capabilities()
	if session.contractVersion == 1 && slices.Contains(session.objects, "save_variables") && caps.Has(connectorapi.VerbFileUpload) && caps.Has(VerbStopPrint) {
		carry(caps, driver.Baseline(), VerbStartPrint, connectorapi.ArgumentKey(VerbStartPrint, "path"))
	}
	heaters := heaterBounds(session.configuration)
	for _, item := range []struct {
		verb connectorapi.Capability
		name string
	}{{VerbNozzleTarget, heaterExtruder}, {VerbBedTarget, heaterBed}} {
		if !caps.Has(item.verb) {
			continue
		}
		bounds := heaters[item.name]
		if bounds.MinTemp == nil || bounds.MaxTemp == nil || math.IsNaN(*bounds.MaxTemp) || math.IsInf(*bounds.MaxTemp, 0) || *bounds.MaxTemp <= *bounds.MinTemp {
			return ConfigurationProfile{}, fmt.Errorf("%s has no valid explicit heater bounds", item.name)
		}
	}
	generated := generatedProfile{Model: identity.Model, Capabilities: caps}
	generated.Firmware.From = identity.Firmware.String()
	generated.Firmware.To = fmt.Sprintf("%s.%s.%d-0", parts[1], parts[2], patch+1)
	registry, err := json.Marshal(struct {
		Schema      int                `json:"schema"`
		GeneratedBy string             `json:"generated_by"`
		Profiles    []generatedProfile `json:"profiles"`
	}{1, "pcma-profile", []generatedProfile{generated}})
	if err != nil {
		return ConfigurationProfile{}, err
	}
	axes := map[string]stepperBounds{}
	for _, axis := range []string{"stepper_x", "stepper_y", "stepper_z"} {
		var bounds stepperBounds
		if json.Unmarshal(session.configuration.Settings[axis], &bounds) == nil && bounds.PositionMax != nil {
			axes[axis] = bounds
		}
	}
	evidence, err := json.Marshal(struct {
		Objects []string
		Heaters map[string]heaterConfig
		Axes    map[string]stepperBounds
	}{session.objects, heaters, axes})
	if err != nil {
		return ConfigurationProfile{}, err
	}
	digest := sha256.Sum256(evidence)
	return ConfigurationProfile{Registry: registry, Model: identity.Model, Firmware: identity.Firmware.String(), ConfigSHA256: hex.EncodeToString(digest[:]), Objects: session.objects, Heaters: heaters, Axes: axes}, nil
}
