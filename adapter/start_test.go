package moonraker

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi/connectorapitest"
	"pcma.local/moonraker/gcode"
	"pcma.local/moonraker/moonrakersim"
)

func startDriver(t *testing.T, statePath string) *Driver {
	t.Helper()
	cfg, _ := json.Marshal(Config{Uploads: true, Starts: true, StatePath: statePath, ProfileID: "ender3", ProfileVersion: "v1", Materials: []string{"PLA", "PETG"}})
	d, err := New(connectorapi.Spec{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	driver := d.(*Driver)
	t.Cleanup(func() { _ = driver.executions.store.db.Close() })
	return driver
}

func TestExecutionDriverContract(t *testing.T) {
	connectorapitest.RunDriverContract(t, func(t *testing.T) connectorapitest.Subject {
		driver := startDriver(t, filepath.Join(t.TempDir(), "execution.db"))
		machine := startMachineWithMacros(t, false)
		return connectorapitest.Subject{Driver: driver, Endpoint: connectorapi.Endpoint{Address: machine.Address()}, Connectable: true, SafeVerb: VerbSendStateInfo}
	})
}

func TestTamperedArtifactAndMissingBedClearBlockStart(t *testing.T) {
	for _, condition := range []string{"tamper", "bed", "contract"} {
		t.Run(condition, func(t *testing.T) {
			m := startMachineWithMacros(t, false)
			d := startDriver(t, filepath.Join(t.TempDir(), "state.db"))
			s := startSession(t, d, m)
			command := preparedUpload(t, s, "job-1")
			switch condition {
			case "tamper":
				m.ReplaceFile("pcma/job-1.gcode", []byte("; changed\n"))
			case "bed":
				m.Push(map[string]map[string]any{evoObject: {"bed_clear_confirmed": false}})
			case "contract":
				m.Push(map[string]map[string]any{evoObject: {"contract_version": 2}})
			}
			if result, err := s.Do(t.Context(), command); err != nil || result.OK {
				t.Fatalf("unsafe start accepted: %+v %v", result, err)
			}
			if m.Called("printer.print.start") {
				t.Fatal("blocked execution reached printer")
			}
		})
	}
}

func TestUnknownStartSurvivesRestartWithoutResending(t *testing.T) {
	m := startMachineWithMacros(t, false)
	filename := filepath.Join(t.TempDir(), "state.db")
	d := startDriver(t, filename)
	s := startSession(t, d, m)
	command := preparedUpload(t, s, "job-unknown")
	a, err := d.executions.store.artifact(t.Context(), command.Args["path"])
	if err != nil {
		t.Fatal(err)
	}
	if err := d.executions.store.confirm(t.Context(), s.identity.UnitUID, "INITIAL", 1); err != nil {
		t.Fatal(err)
	}
	if err := d.executions.store.reserve(t.Context(), s.identity.UnitUID, command.Args["path"], a, "INITIAL", 1, ""); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_ = d.executions.store.db.Close()
	restarted := startDriver(t, filename)
	s2 := startSession(t, restarted, m)
	result, err := s2.Do(t.Context(), command)
	if err != nil || result.OK || result.FaultCode != startUncertain.Code().String() {
		t.Fatalf("unknown execution: %+v %v", result, err)
	}
	if m.Called("printer.print.start") {
		t.Fatal("unknown execution was resent")
	}
}

func TestLocalBedClearIsDurablyAcknowledgedWithoutNextStart(t *testing.T) {
	m := startMachineWithMacros(t, false)
	d := startDriver(t, filepath.Join(t.TempDir(), "state.db"))
	s := startSession(t, d, m)
	command := preparedUpload(t, s, "job-1")
	if result, err := s.Do(t.Context(), command); err != nil || !result.OK {
		t.Fatalf("start: %+v %v", result, err)
	}
	m.Push(map[string]map[string]any{"print_stats": {"state": "complete"}, "pause_resume": {"is_paused": false}, evoObject: {"phase": "FINISHED", "bed_clear_confirmed": true, "bed_clear_for": "job-1", "bed_clear_sequence": int64(2)}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := s.executionSnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Evo.AckFor == "job-1" && snapshot.Evo.AckSequence == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local confirmation did not receive a PC acknowledgement")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var count int
	if err := d.executions.store.db.QueryRow(`SELECT COUNT(*) FROM bed_confirmations WHERE printer=? AND generation='job-1' AND sequence=2`, s.identity.UnitUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("confirmation was not persisted: %d %v", count, err)
	}
}

func startSession(t *testing.T, driver *Driver, machine *moonrakersim.Host) *Session {
	t.Helper()
	s, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address(), Options: map[string]string{"unit_uid": "pcma-sim-printer"}}, driver.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Session)
}

func preparedUpload(t *testing.T, s *Session, id string) connectorapi.Command {
	t.Helper()
	var buffer bytes.Buffer
	_, err := gcode.Prepare(strings.NewReader("G90\n; filament_type = PLA\n"), &buffer, gcode.Options{ExecutionID: id, ProfileID: "ender3", ProfileVersion: "v1", Color: "WHITE"})
	if err != nil {
		t.Fatal(err)
	}
	up := upload(buffer.String())
	up.Path = "gcodes/pcma/" + id + ".gcode"
	if err := s.Send(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return connectorapi.Command{Verb: VerbStartPrint, Args: map[string]string{"path": up.Path}, Key: id}
}

func startMachineWithMacros(t *testing.T, drop bool) *moonrakersim.Host {
	t.Helper()
	m, err := moonrakersim.Start(t.Context(), moonrakersim.Options{PCMA: true, DropStartAck: drop})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestStartKeepsMaterialPauseInSameExecution(t *testing.T) {
	m := startMachineWithMacros(t, false)
	d := startDriver(t, filepath.Join(t.TempDir(), "executions.db"))
	s := startSession(t, d, m)
	command := preparedUpload(t, s, "job-1")
	result, err := s.Do(t.Context(), command)
	if err != nil || !result.OK {
		t.Fatalf("start: %+v %v", result, err)
	}
	snapshot, err := s.executionSnapshot(t.Context())
	if err != nil || snapshot.Evo.ExecutionID != "job-1" || snapshot.Evo.Phase != "MATERIAL_CHANGE" || !snapshot.Paused {
		t.Fatalf("material pause: %+v %v", snapshot, err)
	}
	resume, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbResumePrint})
	if err != nil || resume.OK {
		t.Fatalf("material pause accepted generic resume: %+v %v", resume, err)
	}
	command2 := preparedUpload(t, s, "job-2")
	blocked, err := s.Do(t.Context(), command2)
	if err != nil || blocked.OK {
		t.Fatalf("second execution started: %+v %v", blocked, err)
	}
	duplicate, err := s.Do(t.Context(), command)
	if err != nil || !duplicate.OK {
		t.Fatalf("repeat start: %+v %v", duplicate, err)
	}
	count := 0
	for _, method := range m.Calls() {
		if method == "printer.print.start" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("sent start %d times", count)
	}
}

func TestLostStartAckReconcilesAfterRestart(t *testing.T) {
	m := startMachineWithMacros(t, true)
	filename := filepath.Join(t.TempDir(), "executions.db")
	d := startDriver(t, filename)
	s := startSession(t, d, m)
	command := preparedUpload(t, s, "job-lost-ack")
	if result, err := s.Do(t.Context(), command); err == nil && result.OK {
		t.Fatal("lost ACK was reported accepted")
	}
	_ = s.Close()
	_ = d.executions.store.db.Close()
	restarted := startDriver(t, filename)
	s2 := startSession(t, restarted, m)
	result, err := s2.Do(t.Context(), command)
	if err != nil || !result.OK {
		t.Fatalf("reconciled duplicate: %+v %v", result, err)
	}
	count := 0
	for _, method := range m.Calls() {
		if method == "printer.print.start" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resent uncertain start %d times", count)
	}
}

func TestBedConfirmationGenerationAndConsumption(t *testing.T) {
	m := startMachineWithMacros(t, false)
	d := startDriver(t, filepath.Join(t.TempDir(), "executions.db"))
	s := startSession(t, d, m)
	one := preparedUpload(t, s, "job-1")
	two := preparedUpload(t, s, "job-2")
	if result, err := s.Do(t.Context(), one); err != nil || !result.OK {
		t.Fatalf("first: %+v %v", result, err)
	}
	m.Push(map[string]map[string]any{"print_stats": {"state": "complete"}, "pause_resume": {"is_paused": false}, evoObject: {"phase": "FINISHED", "bed_clear_confirmed": true, "bed_clear_for": "INITIAL", "bed_clear_sequence": int64(2)}})
	if result, err := s.Do(t.Context(), two); err != nil || result.OK {
		t.Fatalf("stale generation: %+v %v", result, err)
	}
	m.Push(map[string]map[string]any{evoObject: {"bed_clear_for": "job-1"}})
	if result, err := s.Do(t.Context(), two); err != nil || !result.OK {
		t.Fatalf("fresh generation: %+v %v", result, err)
	}
}

func TestConcurrentReservationsAllowOneExecution(t *testing.T) {
	ctx := context.Background()
	store, err := openExecutions(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	if err := store.confirm(ctx, "printer", "INITIAL", 1); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"job-a", "job-b"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- store.reserve(ctx, "printer", "gcodes/pcma/"+id+".gcode", gcode.Artifact{Manifest: gcode.Manifest{ExecutionID: id}}, "INITIAL", 1, "")
		}()
	}
	wait.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent reservations", accepted)
	}
}

func TestManagedJobIdentityIgnoresReconnectClock(t *testing.T) {
	first := derivedJobID("pcma/job-1.gcode", time.Unix(100, 0))
	second := derivedJobID("pcma/job-1.gcode", time.Unix(10000, 0))
	if first != "pcma-job-1" || first != second {
		t.Fatalf("unstable managed job ID: %q %q", first, second)
	}
}

func TestChangedExecutionArtifactIsRejectedBeforeUpload(t *testing.T) {
	m := startMachineWithMacros(t, false)
	d := startDriver(t, filepath.Join(t.TempDir(), "state.db"))
	s := startSession(t, d, m)
	preparedUpload(t, s, "job-immutable")
	var changed bytes.Buffer
	_, err := gcode.Prepare(strings.NewReader("G90\n; filament_type = PLA\n"), &changed, gcode.Options{ExecutionID: "job-immutable", ProfileID: "ender3", ProfileVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	up := upload(changed.String())
	up.Path = "gcodes/pcma/job-immutable.gcode"
	if err := s.Send(t.Context(), up); err == nil {
		t.Fatal("existing execution ID accepted different bytes")
	}
	if result, err := s.Do(t.Context(), connectorapi.Command{Verb: VerbStartPrint, Args: map[string]string{"path": up.Path}}); err != nil || !result.OK {
		t.Fatalf("original verified artifact was overwritten: %+v %v", result, err)
	}
}
