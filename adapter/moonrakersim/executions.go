package moonrakersim

import (
	"bytes"
	"encoding/json"
	"maps"
	"strconv"
	"strings"

	"pcma.local/moonraker/gcode"
)

const evoObject = "gcode_macro PCMA_STATE"

func (h *Host) pushCurrent() {
	h.mu.Lock()
	status := map[string]map[string]any{}
	for _, object := range []string{evoObject, "print_stats", "pause_resume"} {
		if fields := h.status[object]; fields != nil {
			status[object] = maps.Clone(fields)
		}
	}
	h.mu.Unlock()
	h.Push(status)
}

func (h *Host) enablePCMA() {
	h.objects = append(h.objects, evoObject, "save_variables")
	h.status["webhooks"] = map[string]any{"state": "ready"}
	h.status["pause_resume"] = map[string]any{"is_paused": false}
	h.status[evoObject] = map[string]any{
		"contract_version": 1, "execution_id": "", "phase": "IDLE",
		"loaded_material": "UNKNOWN", "loaded_color": "UNKNOWN",
		"bed_clear_confirmed": true, "bed_clear_for": "INITIAL", "bed_clear_sequence": int64(1),
		"bed_clear_ack_for": "", "bed_clear_ack_sequence": int64(0),
	}
}

func (h *Host) managedScript(id uint64, params json.RawMessage) map[string]any {
	var args struct {
		Script string `json:"script"`
	}
	if json.Unmarshal(params, &args) != nil {
		return errorFrame(id, 400, "Invalid script")
	}
	if !strings.HasPrefix(args.Script, "PCMA_BED_ACK ") {
		return resultFrame(id, "ok")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	values := map[string]string{}
	for _, token := range strings.Fields(args.Script)[1:] {
		key, value, _ := strings.Cut(token, "=")
		values[key] = value
	}
	sequence, err := strconv.ParseInt(values["SEQUENCE"], 10, 64)
	evo := h.status[evoObject]
	if err != nil || evo == nil || evo["bed_clear_for"] != values["GENERATION"] || evo["bed_clear_sequence"] != sequence || evo["bed_clear_confirmed"] != true {
		return errorFrame(id, 400, "Stale bed acknowledgement")
	}
	evo["bed_clear_ack_for"] = values["GENERATION"]
	evo["bed_clear_ack_sequence"] = sequence
	return resultFrame(id, "ok")
}

func (h *Host) startExecution(id uint64, params json.RawMessage) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var args struct {
		Filename string `json:"filename"`
	}
	if json.Unmarshal(params, &args) != nil {
		return errorFrame(id, 400, "Invalid start")
	}
	stats := h.status["print_stats"]
	if stats["state"] == "printing" || stats["state"] == "paused" {
		return errorFrame(id, 400, "Printer is busy")
	}
	data, exists := h.files[args.Filename]
	if !exists {
		return errorFrame(id, 400, "File not found")
	}
	artifact, err := gcode.Inspect(bytes.NewReader(data))
	evo := h.status[evoObject]
	if err != nil || evo == nil || evo["bed_clear_confirmed"] != true || evo["bed_clear_ack_for"] != evo["bed_clear_for"] || evo["bed_clear_ack_sequence"] != evo["bed_clear_sequence"] {
		return errorFrame(id, 400, "Material hook or bed acknowledgement is missing")
	}
	stats["filename"] = args.Filename
	stats["state"] = "printing"
	stats["total_duration"] = 0.0
	evo["execution_id"] = artifact.ExecutionID
	evo["phase"] = "PRINTING"
	evo["bed_clear_confirmed"] = false
	evo["bed_clear_ack_for"] = ""
	evo["bed_clear_ack_sequence"] = int64(0)
	if evo["loaded_material"] != artifact.Material || evo["loaded_color"] != artifact.Color || artifact.Color == "UNKNOWN" || artifact.Material == "UNKNOWN" {
		evo["phase"] = "MATERIAL_CHANGE"
		stats["state"] = "paused"
		h.status["pause_resume"]["is_paused"] = true
	}
	return resultFrame(id, "ok")
}
