package moonraker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// Manual maintenance verbs use PC's existing controls and command catalogue.
const (
	VerbHome           connectorapi.Capability = "core.command.home"
	VerbMove           connectorapi.Capability = "core.command.move"
	VerbMoveZ          connectorapi.Capability = "core.command.move_z"
	VerbMesh           connectorapi.Capability = "core.command.mesh_bed_leveling"
	VerbLoadFilament   connectorapi.Capability = "core.command.load_filament"
	VerbUnloadFilament connectorapi.Capability = "core.command.unload_filament"
)

func manualVerb(verb connectorapi.Capability) bool {
	return verb == VerbHome || verb == VerbMove || verb == VerbMoveZ || verb == VerbMesh || filamentVerb(verb)
}

func filamentVerb(verb connectorapi.Capability) bool {
	return verb == VerbLoadFilament || verb == VerbUnloadFilament
}

func manualBaseline(caps connectorapi.Set) {
	for _, verb := range []connectorapi.Capability{VerbHome, VerbMove, VerbMoveZ, VerbMesh, VerbLoadFilament, VerbUnloadFilament} {
		caps[verb] = connectorapi.Value{Flag: true}
	}
	number := connectorapi.Value{Flag: true, Kind: connectorapi.KindNumber, Min: -100, HasMin: true, Limit: 100, HasLimit: true, Unit: connectorapi.UnitMillimeter}
	for _, axis := range []string{"x", "y"} {
		caps[connectorapi.ArgumentKey(VerbMove, axis)] = number
	}
	number.Required = true
	caps[connectorapi.ArgumentKey(VerbMoveZ, "distance")] = number
	for _, verb := range []connectorapi.Capability{VerbMove, VerbMoveZ} {
		limit := 6000.0
		if verb == VerbMoveZ {
			limit = 600
		}
		caps[connectorapi.ArgumentKey(verb, "feedrate")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindInteger, Required: true, Min: 1, HasMin: true, Limit: limit, HasLimit: true, Unit: connectorapi.UnitMillimeterPerMinute}
	}
	caps[connectorapi.ArgumentKey(VerbLoadFilament, "mode")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Supported: []string{"load", "change"}, Default: "load"}
	for _, argument := range []string{"filament", "color"} {
		caps[connectorapi.ArgumentKey(VerbLoadFilament, argument)] = connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Limit: 32, HasLimit: true}
	}
	caps[connectorapi.ArgumentKey(VerbLoadFilament, "request_id")] = connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Limit: 64, HasLimit: true}
}

func (s *Session) probeManual(offered connectorapi.Set) {
	if !slices.Contains(s.objects, objectToolhead) || !slices.Contains(s.objects, "webhooks") || !slices.Contains(s.objects, objectPrintStats) || !slices.Contains(s.objects, "idle_timeout") {
		return
	}
	carry(s.probe, offered, VerbHome)
	// Relative jogging is limited to machines with Cartesian axis bounds.
	if _, ok := s.axisBounds(); ok {
		carry(s.probe, offered, VerbMove, VerbMoveZ)
		for key := range offered {
			if strings.HasPrefix(key.String(), VerbMove.String()+"#") || strings.HasPrefix(key.String(), VerbMoveZ.String()+"#") {
				carry(s.probe, offered, key)
			}
		}
	}
	if slices.Contains(s.objects, "bed_mesh") && slices.Contains(s.objects, "probe") {
		carry(s.probe, offered, VerbMesh)
	}
	if len(extrudersOf(s.objects)) == 1 {
		if slices.Contains(s.objects, "gcode_macro LOAD_FILAMENT") || slices.Contains(s.objects, "gcode_macro PCMA_LOAD_FILAMENT") || slices.Contains(s.objects, "gcode_macro PCMA_FILAMENT_CHANGE") {
			carry(s.probe, offered, VerbLoadFilament, connectorapi.ArgumentKey(VerbLoadFilament, "mode"), connectorapi.ArgumentKey(VerbLoadFilament, "filament"), connectorapi.ArgumentKey(VerbLoadFilament, "color"), connectorapi.ArgumentKey(VerbLoadFilament, "request_id"))
		}
		if slices.Contains(s.objects, "gcode_macro UNLOAD_FILAMENT") || slices.Contains(s.objects, "gcode_macro PCMA_UNLOAD_FILAMENT") {
			carry(s.probe, offered, VerbUnloadFilament)
		}
	}
}

func (s *Session) axisBounds() ([3][2]float64, bool) {
	var bounds [3][2]float64
	for i, axis := range []string{"stepper_x", "stepper_y", "stepper_z"} {
		var configured stepperBounds
		if json.Unmarshal(s.configuration.Settings[axis], &configured) != nil || configured.PositionMax == nil {
			return bounds, false
		}
		low := 0.0
		if configured.PositionMin != nil {
			low = *configured.PositionMin
		}
		high := *configured.PositionMax
		if !finite(low) || !finite(high) || high <= low {
			return bounds, false
		}
		bounds[i] = [2]float64{low, high}
	}
	return bounds, true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// manualCommand always obtains a fresh machine reading before an actuator call.
// It does not retry scripts: a lost answer may mean the action already started.
func (s *Session) manualCommand(ctx context.Context, cmd connectorapi.Command) (connectorapi.Result, error) {
	validated, err := s.caps.Validate(cmd)
	if err != nil {
		return connectorapi.Result{}, err
	}
	cmd = validated
	raw, refusal, err := s.call(ctx, methodObjectsQuery, objectsParam(present([]string{"webhooks", "print_stats", "pause_resume", "idle_timeout", "toolhead", evoObject}, s.objects)))
	if err != nil {
		return connectorapi.Result{}, err
	}
	if refusal != nil {
		return manualRefused("printer status query refused"), nil
	}
	var reply statusReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		return connectorapi.Result{}, err
	}
	fields := objectFields(reply.Status)
	if cmd.Verb == VerbLoadFilament && cmd.Args["mode"] == "change" {
		phase := klipperText(fields[evoObject]["filament_phase"])
		if slices.Contains([]string{"REQUESTED", "UNLOADING", "LOADING", "AWAITING_INSERTION", "AWAITING_CONFIRMATION", "RECOVERY_REQUIRED"}, phase) {
			return manualRefused("a filament change is already pending on the printer"), nil
		}
	}
	if klipperText(fields["webhooks"]["state"]) != "ready" {
		return manualRefused("printer is not ready"), nil
	}
	state := klipperText(fields["print_stats"]["state"])
	var paused bool
	if slices.Contains(s.objects, objectPauseResume) && json.Unmarshal(fields[objectPauseResume]["is_paused"], &paused) != nil {
		return manualRefused("pause state is unknown"), nil
	}
	filamentPause := filamentVerb(cmd.Verb) && state == "paused" && paused
	if !filamentPause {
		if paused || (state != "standby" && state != "complete" && state != "cancelled") {
			return manualRefused("manual action requires an idle printer"), nil
		}
		idle := klipperText(fields["idle_timeout"]["state"])
		if idle != "Idle" && idle != "Ready" {
			return manualRefused("printer is processing another action"), nil
		}
	}
	script := ""
	switch cmd.Verb {
	case VerbHome:
		script = "G28"
	case VerbMesh:
		script = "G28\nBED_MESH_CALIBRATE"
		if slices.Contains(s.objects, "gcode_macro G29") {
			script = "G28\nG29"
		}
	case VerbLoadFilament:
		script, err = filamentScript(cmd)
		if err != nil {
			return manualRefused(err.Error()), nil
		}
		if script == "LOAD_FILAMENT" && slices.Contains(s.objects, "gcode_macro PCMA_LOCAL_LOAD") {
			script = "PCMA_LOCAL_LOAD"
		}
	case VerbUnloadFilament:
		script = "UNLOAD_FILAMENT"
		if slices.Contains(s.objects, evoObject) || slices.Contains(s.objects, "gcode_macro PCMA_UNLOAD_FILAMENT") {
			script = "PCMA_UNLOAD_FILAMENT"
		}
	case VerbMove, VerbMoveZ:
		var position []float64
		if json.Unmarshal(fields[objectToolhead]["position"], &position) != nil || len(position) < 3 {
			return manualRefused("axis position is unknown"), nil
		}
		homed := klipperText(fields[objectToolhead]["homed_axes"])
		bounds, ok := s.axisBounds()
		if !ok {
			return manualRefused("axis bounds are unknown"), nil
		}
		var axes string
		count := 0
		for i, axis := range []string{"x", "y", "z"} {
			key := axis
			if cmd.Verb == VerbMoveZ {
				if axis != "z" {
					continue
				}
				key = "distance"
			} else if axis == "z" {
				continue
			}
			value, exists := cmd.Args[key]
			if !exists {
				continue
			}
			distance, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil || !finite(distance) {
				return manualRefused("invalid movement distance"), nil
			}
			if !strings.Contains(homed, axis) || !finite(position[i]) || position[i]+distance < bounds[i][0] || position[i]+distance > bounds[i][1] {
				return manualRefused("home the requested axis and stay within its limits"), nil
			}
			axes += " " + strings.ToUpper(axis) + strconv.FormatFloat(distance, 'f', -1, 64)
			count++
		}
		if count == 0 {
			return manualRefused("movement requires an axis"), nil
		}
		feed, err := strconv.Atoi(cmd.Args["feedrate"])
		if err != nil {
			return manualRefused("invalid feedrate"), nil
		}
		script = fmt.Sprintf("SAVE_GCODE_STATE NAME=PCMA_MANUAL_JOG\nG91\nG1%s F%d\nM400\nRESTORE_GCODE_STATE NAME=PCMA_MANUAL_JOG", axes, feed)
	}
	answer, refusal, err := s.callWithTimeout(ctx, methodGcodeScript, map[string]any{"script": script}, max(s.timeout, 5*time.Minute))
	if err != nil {
		return connectorapi.Result{}, err
	}
	if refusal != nil {
		return connectorapi.Result{OK: false, MachineReason: refusal.Message}, nil
	}
	if strings.HasPrefix(script, "PCMA_") && !slices.Contains(s.objects, "gcode_macro "+strings.Fields(script)[0]) {
		return manualRefused("the requested filament macro is not installed on the printer"), nil
	}
	return connectorapi.Result{OK: true, Raw: answer}, nil
}

func manualRefused(reason string) connectorapi.Result {
	return connectorapi.Result{OK: false, MachineReason: reason}
}

var manualMaterialID = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,31}$`)
var manualRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func filamentScript(cmd connectorapi.Command) (string, error) {
	mode := cmd.Args["mode"]
	material := strings.ToUpper(cmd.Args["filament"])
	color := strings.ToUpper(cmd.Args["color"])
	if (mode == "" || mode == "load") && material == "" && color == "" {
		if cmd.Args["request_id"] != "" {
			return "", fmt.Errorf("a filament request ID requires material and color")
		}
		return "LOAD_FILAMENT", nil
	}
	if strings.HasPrefix(color, "#") {
		if len(color) != 7 {
			return "", fmt.Errorf("color needs six hexadecimal digits")
		}
		if _, err := hex.DecodeString(color[1:]); err != nil {
			return "", fmt.Errorf("invalid color")
		}
		color = "C_" + color[1:]
	}
	if !manualMaterialID.MatchString(material) || !manualMaterialID.MatchString(color) || material == "UNKNOWN" || color == "UNKNOWN" {
		return "", fmt.Errorf("material and color are required for a confirmed filament workflow")
	}
	macro := "PCMA_LOAD_FILAMENT"
	if mode == "change" {
		macro = "PCMA_FILAMENT_CHANGE"
	}
	script := fmt.Sprintf("%s MATERIAL=%s COLOR=%s", macro, material, color)
	if request := cmd.Args["request_id"]; request != "" {
		if !manualRequestID.MatchString(request) {
			return "", fmt.Errorf("invalid filament request ID")
		}
		script += " REQUEST=" + request
	}
	return script, nil
}
