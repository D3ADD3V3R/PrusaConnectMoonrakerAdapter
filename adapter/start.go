package moonraker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"pcma.local/moonraker/gcode"
)

type evoState struct {
	ChangeID          string `json:"change_id"`
	ConfirmedChangeID string `json:"confirmed_change_id"`
	FilamentPhase     string `json:"filament_phase"`
	ContractVersion   int    `json:"contract_version"`
	ExecutionID       string `json:"execution_id"`
	Phase             string `json:"phase"`
	BedClear          bool   `json:"bed_clear_confirmed"`
	BedFor            string `json:"bed_clear_for"`
	BedSequence       int64  `json:"bed_clear_sequence"`
	AckFor            string `json:"bed_clear_ack_for"`
	AckSequence       int64  `json:"bed_clear_ack_sequence"`
}

type executionSnapshot struct {
	Evo        evoState
	File       string
	PrintState string
	Ready      bool
	Paused     bool
}

const evoObject = "gcode_macro PCMA_STATE"

type executionRuntime struct {
	store          *executionStore
	profile        string
	profileVersion string
	materials      []string
}

func validateExecutionConfig(config Config) error {
	for _, material := range config.Materials {
		if material == "UNKNOWN" {
			return fmt.Errorf("UNKNOWN cannot be an approved material profile")
		}
		manifest := gcode.Manifest{Version: 1, ExecutionID: "config-check", ProfileID: config.ProfileID, ProfileVersion: config.ProfileVersion, OriginalSHA256: strings.Repeat("0", 64), Material: material, Color: "UNKNOWN"}
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("profile IDs and material IDs must use the prepared-artifact format: %w", err)
		}
	}
	return nil
}

func (s *Session) executionSnapshot(ctx context.Context) (executionSnapshot, error) {
	raw, refusal, err := s.call(ctx, methodObjectsQuery, objectsParam([]string{evoObject, "print_stats", "webhooks", "pause_resume"}))
	if err != nil {
		return executionSnapshot{}, err
	}
	if refusal != nil {
		return executionSnapshot{}, fmt.Errorf("execution query refused: %s", refusal.Message)
	}
	var reply statusReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return executionSnapshot{}, err
	}
	var evo evoState
	if err := json.Unmarshal(reply.Status[evoObject], &evo); err != nil {
		return executionSnapshot{}, err
	}
	fields := objectFields(reply.Status)
	var paused bool
	if err := json.Unmarshal(fields["pause_resume"]["is_paused"], &paused); err != nil {
		return executionSnapshot{}, err
	}
	return executionSnapshot{Evo: evo, File: klipperText(fields["print_stats"]["filename"]), PrintState: klipperText(fields["print_stats"]["state"]), Ready: klipperText(fields["webhooks"]["state"]) == "ready", Paused: paused}, nil
}

func (s *Session) inspectUpload(ctx context.Context, upload connectorapi.Upload) (*gcode.Artifact, error) {
	if s.executions == nil {
		return nil, nil
	}
	reader, err := upload.Content.Open(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	artifact, err := gcode.Inspect(&contextReader{ctx, reader})
	if err != nil {
		return nil, err
	}
	if artifact.ProfileID != s.executions.profile || artifact.ProfileVersion != s.executions.profileVersion {
		return nil, fmt.Errorf("artifact compatibility profile differs from this printer's configured revision")
	}
	allowed := false
	for _, material := range s.executions.materials {
		if artifact.Material == material {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("material is outside the local approved profiles")
	}
	if upload.Path != "gcodes/pcma/"+artifact.ExecutionID+".gcode" {
		return nil, fmt.Errorf("prepared artifact path must be gcodes/pcma/<execution_id>.gcode")
	}
	if err := s.executions.store.claim(ctx, upload.Path, artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (s *Session) reconcileExecution(ctx context.Context, snapshot executionSnapshot) error {
	if snapshot.Evo.ContractVersion != 1 {
		return fmt.Errorf("missing or incompatible PCMA macro contract")
	}
	printer := s.identity.UnitUID
	previous, err := s.executions.store.latest(ctx, printer)
	if err != nil && !missingRecord(err) {
		return err
	}
	if err == nil && previous.ExecutionID == snapshot.Evo.ExecutionID && strings.TrimPrefix(previous.Path, "gcodes/") == snapshot.File {
		state := ""
		switch snapshot.PrintState {
		case "printing", "paused":
			state = "ACKNOWLEDGED"
		case "complete":
			state = "FINISHED"
		case "error":
			state = "FAILED"
		case "cancelled":
			state = "CANCELLED"
		}
		if state != "" && !terminalExecution(previous.State) {
			if err := s.executions.store.setState(ctx, previous.ExecutionID, state); err != nil {
				return err
			}
			previous.State = state
		}
	}
	if snapshot.Evo.ContractVersion != 1 || !snapshot.Ready || snapshot.Paused || snapshot.PrintState == "printing" || snapshot.PrintState == "paused" || !snapshot.Evo.BedClear {
		return nil
	}
	expected := "INITIAL"
	if previous.ExecutionID != "" {
		if !terminalExecution(previous.State) {
			return nil
		}
		expected = previous.ExecutionID
		if snapshot.Evo.ExecutionID != expected {
			return nil
		}
	} else if snapshot.Evo.ExecutionID != "" {
		return nil
	}
	if snapshot.Evo.BedFor != expected {
		return nil
	}
	if err := s.executions.store.confirm(ctx, printer, expected, snapshot.Evo.BedSequence); err != nil {
		return err
	}
	if snapshot.Evo.AckFor == expected && snapshot.Evo.AckSequence == snapshot.Evo.BedSequence {
		return nil
	}
	_, refusal, err := s.call(ctx, methodGcodeScript, map[string]any{"script": fmt.Sprintf("PCMA_BED_ACK GENERATION=%s SEQUENCE=%d", expected, snapshot.Evo.BedSequence)})
	if err != nil {
		return err
	}
	if refusal != nil {
		return fmt.Errorf("bed acknowledgement refused: %s", refusal.Message)
	}
	return nil
}

func (s *Session) startPrint(ctx context.Context, command connectorapi.Command) (connectorapi.Result, error) {
	if !s.uploads.transfer.TryLock() {
		return blockedStart("another file or start operation is in progress"), nil
	}
	defer s.uploads.transfer.Unlock()
	name := command.Args["path"]
	if _, err := destination(name); err != nil {
		return blockedStart(err.Error()), nil
	}
	artifact, err := s.executions.store.artifact(ctx, name)
	if err != nil {
		return blockedStart("file has no verified execution artifact"), nil
	}
	if command.Key != "" && command.Key != artifact.ExecutionID {
		return blockedStart("idempotency key differs from the artifact execution ID"), nil
	}
	snapshot, err := s.executionSnapshot(ctx)
	if err != nil {
		return connectorapi.Result{}, err
	}
	if err := s.reconcileExecution(ctx, snapshot); err != nil {
		return blockedStart(err.Error()), nil
	}
	previous, err := s.executions.store.find(ctx, artifact.ExecutionID)
	if err == nil {
		if previous.Printer != s.identity.UnitUID {
			return blockedStart("execution belongs to a different printer"), nil
		}
		if previous.State == "ACKNOWLEDGED" || previous.State == "FINISHED" || previous.State == "FAILED" || previous.State == "CANCELLED" {
			raw, _ := json.Marshal(map[string]any{"execution_id": artifact.ExecutionID, "state": previous.State, "duplicate": true})
			return connectorapi.Result{OK: true, Raw: raw}, nil
		}
		return connectorapi.Result{OK: false, FaultCode: startUncertain.Code().String(), MachineReason: "the existing execution needs reconciliation; no start was resent"}, nil
	}
	if !missingRecord(err) {
		return connectorapi.Result{}, err
	}
	snapshot, err = s.executionSnapshot(ctx)
	if err != nil {
		return connectorapi.Result{}, err
	}
	if snapshot.Evo.ContractVersion != 1 || !snapshot.Ready || snapshot.Paused || snapshot.PrintState == "printing" || snapshot.PrintState == "paused" || !snapshot.Evo.BedClear || snapshot.Evo.AckFor != snapshot.Evo.BedFor || snapshot.Evo.AckSequence != snapshot.Evo.BedSequence {
		return blockedStart("printer or local bed confirmation is not ready"), nil
	}
	expectedHash, err := hex.DecodeString(artifact.SHA256)
	if err != nil {
		return blockedStart("invalid artifact receipt"), nil
	}
	if err := s.verify(ctx, strings.TrimPrefix(name, "gcodes/"), artifact.Size, expectedHash); err != nil {
		return blockedStart("stored execution artifact changed after upload"), nil
	}
	if err := s.executions.store.reserve(ctx, s.identity.UnitUID, name, artifact, snapshot.Evo.BedFor, snapshot.Evo.BedSequence, snapshot.Evo.ExecutionID); err != nil {
		return blockedStart("printer reservation or bed confirmation is already in use"), nil
	}
	raw, refusal, err := s.call(ctx, "printer.print.start", map[string]any{"filename": strings.TrimPrefix(name, "gcodes/")})
	if err != nil {
		// START_REQUESTED was committed before the call and remains for reconciliation.
		return connectorapi.Result{}, startUncertain.New("the printer did not conclusively acknowledge this start")
	}
	if refusal != nil {
		if err := s.executions.store.refuse(ctx, artifact.ExecutionID); err != nil {
			return connectorapi.Result{}, err
		}
		return blockedStart(refusal.Message), nil
	}
	var acknowledgement string
	if json.Unmarshal(raw, &acknowledgement) != nil || acknowledgement != "ok" {
		return connectorapi.Result{}, startUncertain.New("the printer returned an unrecognized start acknowledgement")
	}
	if err := s.executions.store.setState(ctx, artifact.ExecutionID, "ACKNOWLEDGED"); err != nil {
		return connectorapi.Result{}, err
	}
	return connectorapi.Result{OK: true, Raw: raw}, nil
}

func blockedStart(reason string) connectorapi.Result {
	return connectorapi.Result{OK: false, FaultCode: startBlocked.Code().String(), MachineReason: reason}
}

func (s *Session) watchExecutions() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		snapshot, err := s.executionSnapshot(ctx)
		if err == nil {
			err = s.reconcileExecution(ctx, snapshot)
		}
		cancel()
		if err != nil {
			s.log.Debug("execution reconciliation is pending", "error", err)
		}
	}
}
