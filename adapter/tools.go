package moonraker

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// Klipper names its extruders `extruder`, `extruder1`, `extruder2` and on
// (klippy/kinematics/extruder.py). Each one is a head of the tool tree:
// `extruder` is head "0", `extruder1` is head "1".
//
// Native Klipper reports no loaded material. The optional PCMA contract supplies
// locally confirmed material and color for a single extruder.

// extruderPrefix is the name every extruder object starts with.
const extruderPrefix = "extruder"

func confirmedFilamentPatches(fields map[string]json.RawMessage) []connectorapi.StatePatch {
	var out []connectorapi.StatePatch
	for _, item := range []struct{ field, member string }{{"loaded_material", connectorapi.MaterialAbbreviation}, {"loaded_color", connectorapi.MaterialColor}} {
		raw, present := fields[item.field]
		if !present {
			continue
		}
		value := klipperText(raw)
		encoded := json.RawMessage(`null`)
		if item.field == "loaded_material" && value != "UNKNOWN" && manualMaterialID.MatchString(value) {
			encoded, _ = json.Marshal(value)
		}
		if item.field == "loaded_color" && strings.HasPrefix(value, "C_") {
			color := "#" + strings.TrimPrefix(value, "C_")
			if connectorapi.ValidColor(color) {
				encoded, _ = json.Marshal(color)
			}
		}
		out = append(out, connectorapi.StatePatch{Path: connectorapi.ToolPath("0", item.member), Value: encoded})
		if item.field == "loaded_material" {
			out = append(out, connectorapi.StatePatch{Path: connectorapi.ToolPath("0", connectorapi.MaterialType), Value: encoded})
		}
	}
	return out
}

// toolOf is the tool index an object name is, counted from 0, and false for
// an object that is not an extruder.
func toolOf(object string) (int, bool) {
	rest, found := strings.CutPrefix(object, extruderPrefix)
	if !found {
		return 0, false
	}

	if rest == "" {
		return 0, true
	}

	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 || strconv.Itoa(n) != rest {
		return 0, false
	}

	return n, true
}

// extruderName is the extruder object a head is, the reverse of toolOf.
func extruderName(tool int) string {
	if tool == 0 {
		return extruderPrefix
	}

	return extruderPrefix + strconv.Itoa(tool)
}

// extrudersOf is the extruder objects a machine lists, in tool order.
func extrudersOf(objects []string) []string {
	var out []string

	for _, object := range objects {
		if _, ok := toolOf(object); ok {
			out = append(out, object)
		}
	}

	slices.SortFunc(out, func(a, b string) int {
		ta, _ := toolOf(a)
		tb, _ := toolOf(b)

		return ta - tb
	})

	return out
}

// extruderConfig is the part of an extruder section this driver reads.
type extruderConfig struct {
	NozzleDiameter json.RawMessage `json:"nozzle_diameter"`
}

// toolPatches reports the tools of the configuration: the head count, each
// head enabled, each nozzle diameter. A single head is always active.
func toolPatches(dumped configDump, extruders []string) []connectorapi.StatePatch {
	if len(extruders) == 0 {
		return nil
	}

	out := []connectorapi.StatePatch{{
		Path:  connectorapi.ToolCountPath,
		Value: json.RawMessage(strconv.Itoa(len(extruders))),
	}}

	if len(extruders) == 1 {
		out = append(out, connectorapi.StatePatch{
			Path: connectorapi.ToolPath("0", connectorapi.ToolActive), Value: json.RawMessage(`true`),
		})
	}

	for _, object := range extruders {
		tool, _ := toolOf(object)
		head := connectorapi.HeadID(tool)

		out = append(
			out,
			connectorapi.StatePatch{
				Path:  connectorapi.ToolPath(head, connectorapi.ToolEnabled),
				Value: json.RawMessage(`true`),
			},
			connectorapi.StatePatch{
				Path:  connectorapi.ToolPath(head, connectorapi.ToolType),
				Value: json.RawMessage(strconv.Quote(connectorapi.ToolTypeHead)),
			},
		)

		var configured extruderConfig
		if json.Unmarshal(dumped.Settings[object], &configured) != nil || len(configured.NozzleDiameter) == 0 {
			continue
		}

		out = append(out, connectorapi.StatePatch{
			Path:  connectorapi.ToolPath(head, connectorapi.FeatureNozzleDiameter),
			Value: configured.NozzleDiameter,
		})
	}

	return out
}

// heads is what this driver knows about the heads between two readings. The
// readings of every extruder go under its head, also with one extruder.
type heads struct {
	// count is how many extruders the machine lists.
	count int
}

// appendReadings writes each extruder's nozzle readings under its head. With
// several heads, toolhead.extruder says which one is active.
func (h *heads) appendReadings(
	out []connectorapi.StatePatch, objects map[string]map[string]json.RawMessage,
) []connectorapi.StatePatch {
	if active, named := toolOf(klipperText(objects[objectToolhead]["extruder"])); named {
		for tool := 0; h.count > 1 && tool < h.count; tool++ {
			out = append(out, connectorapi.StatePatch{
				Path:  connectorapi.ToolPath(connectorapi.HeadID(tool), connectorapi.ToolActive),
				Value: json.RawMessage(strconv.FormatBool(tool == active)),
			})
		}
	}

	names := make([]string, 0, len(objects))
	for object := range objects {
		names = append(names, object)
	}

	for _, object := range extrudersOf(names) {
		tool, _ := toolOf(object)
		head := connectorapi.HeadID(tool)

		for field, member := range map[string]string{
			"temperature": connectorapi.NozzleTemp, "target": connectorapi.NozzleTargetTemp,
		} {
			if reading := objects[object][field]; len(reading) > 0 {
				out = append(out, connectorapi.StatePatch{Path: connectorapi.ToolPath(head, member), Value: reading})
			}
		}
	}

	return out
}
