package moonraker

import (
	"encoding/json"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"pcma.local/moonraker/moonrakersim"
)

func fileSession(t *testing.T, options moonrakersim.Options) (*Session, *moonrakersim.Host) {
	t.Helper()
	machine, err := moonrakersim.Start(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close() })
	driver := NewDriver(0)
	s, err := driver.Connect(t.Context(), connectorapi.Endpoint{Address: machine.Address()}, driver.Baseline())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Session), machine
}

func TestFileTreeAndMutations(t *testing.T) {
	s, _ := fileSession(t, moonrakersim.Options{Files: map[string][]byte{"jobs/part.gcode": []byte("; file\n")}})
	roots, err := s.Storages(t.Context())
	if err != nil || len(roots) != 1 || roots[0].Name != "gcodes" {
		t.Fatalf("storages: %+v, %v", roots, err)
	}
	listing, err := s.List(t.Context(), "gcodes/jobs")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Path != "gcodes/jobs/part.gcode" {
		t.Fatalf("listing: %+v, %v", listing, err)
	}
	file, err := s.List(t.Context(), "gcodes/jobs/part.gcode")
	if err != nil || file.Directory.Folder || file.Directory.Size != 7 {
		t.Fatalf("file: %+v, %v", file, err)
	}
	if err := s.CreateFolder(t.Context(), "gcodes/new"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFolder(t.Context(), "gcodes/new"); err == nil {
		t.Fatal("duplicate directory creation accepted")
	}
	if err := s.DeleteFolder(t.Context(), "gcodes/jobs"); err == nil {
		t.Fatal("nonempty directory deleted")
	}
	if err := s.DeleteFile(t.Context(), "gcodes/jobs/part.gcode"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(t.Context(), "gcodes/jobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(t.Context(), "gcodes/jobs/part.gcode"); err == nil {
		t.Fatal("deleted file is still present")
	}
}

func TestReadOnlyStorageAndPathIsolation(t *testing.T) {
	s, machine := fileSession(t, moonrakersim.Options{ReadOnly: true})
	caps, _ := s.Capabilities()
	if caps.Has(connectorapi.VerbCreateFolder) || caps.Has(connectorapi.VerbDeleteFile) {
		t.Fatal("read-only root offered mutations")
	}
	if _, err := s.List(t.Context(), "gcodes"); err != nil {
		t.Fatal(err)
	}
	before := len(machine.Calls())
	for _, value := range []string{"config", "gcodes/../config", "gcodes/x/../../config", "gcodes/%2e%2e", "gcodes/a\\b", "gcodes//x"} {
		if _, err := s.List(t.Context(), value); err == nil {
			t.Errorf("invalid path accepted: %q", value)
		}
	}
	if got := len(machine.Calls()); got != before {
		t.Fatalf("invalid path reached Moonraker: %d -> %d", before, got)
	}
}

func TestRejectsInvalidDirectoryEntries(t *testing.T) {
	for _, name := range []string{"../config", "x/y", "%2e%2e", ".", "", "x\\y"} {
		data, err := json.Marshal(map[string]any{"files": []any{map[string]any{"filename": name, "size": 1, "permissions": "rw"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := directoryListing("gcodes", data); err == nil {
			t.Errorf("accepted malicious listing %q", name)
		}
	}
}
