package macroconfig

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type machine struct {
	files            map[string][]byte
	settings         map[string]any
	objects          []string
	state            string
	readOnly         bool
	writes, restarts int
	failMainAck      bool
}

func fake(t *testing.T) (*machine, *Setup, string) {
	t.Helper()
	m := &machine{files: map[string][]byte{"printer.cfg": []byte("[extruder]\nmax_temp: 250\n\n#*# <---------------------- SAVE_CONFIG ---------------------->\n#*# data\n")}, settings: map[string]any{"extruder": map[string]any{"max_temp": 250}}, objects: []string{"configfile", "extruder", "idle_timeout", "print_stats", "pause_resume"}, state: "standby"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		result := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"result": v}) }
		switch r.URL.Path {
		case "/printer/objects/list":
			result(map[string]any{"objects": m.objects})
		case "/printer/objects/query":
			result(map[string]any{"status": map[string]any{"configfile": map[string]any{"settings": m.settings}, "print_stats": map[string]any{"state": m.state}, "pause_resume": map[string]bool{"is_paused": m.state == "paused"}, "idle_timeout": map[string]any{"state": "Ready"}}})
		case "/server/files/roots":
			permission := "rw"
			if m.readOnly {
				permission = "r"
			}
			result([]map[string]string{{"name": "config", "path": "/printer/config", "permissions": permission}})
		case "/printer/info":
			result(map[string]string{"config_file": "/printer/config/printer.cfg"})
		case "/server/files/list":
			list := []map[string]string{}
			for name := range m.files {
				list = append(list, map[string]string{"path": name})
			}
			result(list)
		case "/server/files/copy":
			var p map[string]string
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			m.files[strings.TrimPrefix(p["dest"], "config/")] = append([]byte(nil), m.files[strings.TrimPrefix(p["source"], "config/")]...)
			m.writes++
			result("ok")
		case "/server/files/upload":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
				return
			}
			file, head, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			b, err := io.ReadAll(file)
			if err != nil {
				t.Error(err)
				return
			}
			if r.FormValue("root") != "config" || r.FormValue("print") != "false" {
				t.Error("wrong upload root or print policy")
			}
			m.files[head.Filename] = b
			m.writes++
			if head.Filename == "printer.cfg" && m.failMainAck {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			result("ok")
		case "/printer/restart":
			m.restarts++
			result("ok")
		default:
			if strings.HasPrefix(r.URL.Path, "/server/files/config/") {
				b, ok := m.files[strings.TrimPrefix(r.URL.Path, "/server/files/config/")]
				if !ok {
					w.WriteHeader(404)
					return
				}
				_, _ = w.Write(b)
				return
			}
			t.Error("unexpected request: " + r.Method + " " + r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return m, &Setup{Client: server.Client()}, server.URL
}

func TestInstallBacksUpAndVerifiesWithoutRestart(t *testing.T) {
	m, s, base := fake(t)
	before := string(m.files["printer.cfg"])
	status, err := s.Status(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installable || status.Ready {
		t.Fatalf("unexpected status: %+v", status)
	}
	installed, err := s.Install(t.Context(), base, status.ConfigDigest, status.BundleDigest)
	if err != nil {
		t.Fatal(err)
	}
	if m.restarts != 0 || !installed.DiskInstalled || !installed.RestartAllowed {
		t.Fatalf("unexpected install: %+v", installed)
	}
	if string(m.files[installed.Backup]) != before {
		t.Fatal("backup differs")
	}
	main := string(m.files["printer.cfg"])
	if strings.Index(main, includeLine(Filename)) >= strings.Index(main, "#*# <") {
		t.Fatal("include landed after SAVE_CONFIG")
	}
	writes := m.writes
	if _, err = s.Install(t.Context(), base, installed.ConfigDigest, installed.BundleDigest); err != nil {
		t.Fatal(err)
	}
	if m.writes != writes {
		t.Fatal("repeat install wrote again")
	}
	m.state = "paused"
	if err = s.Restart(t.Context(), base, installed.ConfigDigest); err == nil {
		t.Fatal("restarted a paused printer")
	}
	m.state = "standby"
	if err = s.Restart(t.Context(), base, installed.ConfigDigest); err != nil {
		t.Fatal(err)
	}
	if m.restarts != 1 {
		t.Fatal("restart was not explicit and singular")
	}
}

func TestRefuseUnsafePlansBeforeWriting(t *testing.T) {
	for _, reason := range []string{"read_only", "custom_resume", "printing", "missing_state", "partial_macro", "lowercase_macro", "delayed_macro", "file_conflict", "stale_digest"} {
		t.Run(reason, func(t *testing.T) {
			m, s, base := fake(t)
			switch reason {
			case "read_only":
				m.readOnly = true
			case "custom_resume":
				m.settings["gcode_macro resume"] = map[string]any{"gcode": "RESUME_BASE"}
			case "printing":
				m.state = "printing"
			case "missing_state":
				m.state = ""
			case "partial_macro":
				m.objects = append(m.objects, "gcode_macro PCMA_LOAD_FILAMENT")
			case "lowercase_macro":
				m.objects = append(m.objects, "gcode_macro pcma_load_filament")
			case "delayed_macro":
				m.settings["delayed_gcode _pcma_boot"] = map[string]string{"gcode": "USER_BOOT"}
			case "file_conflict":
				m.files[Filename] = []byte("user content")
			}
			plan, err := s.Status(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "stale_digest" {
				m.files["printer.cfg"] = append(m.files["printer.cfg"], []byte("\n# changed")...)
			}
			if _, err = s.Install(t.Context(), base, plan.ConfigDigest, plan.BundleDigest); err == nil {
				t.Fatal("unsafe plan accepted")
			}
			if m.writes != 0 || m.restarts != 0 {
				t.Fatal("unsafe plan changed printer")
			}
		})
	}
}

func TestInstalledFileDoesNotPermitReplacingCustomResumeOnRestart(t *testing.T) {
	m, s, base := fake(t)
	m.files[Filename] = append([]byte(nil), Filament...)
	m.files["printer.cfg"] = addInclude(m.files["printer.cfg"])
	m.settings["gcode_macro resume"] = map[string]string{"gcode": "CUSTOM_RESUME"}
	status, err := s.Status(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if status.RestartAllowed || status.Reason != "custom_resume_requires_merge" {
		t.Fatalf("unsafe restart plan: %+v", status)
	}
	if err = s.Restart(t.Context(), base, status.ConfigDigest); err == nil || m.restarts != 0 {
		t.Fatal("custom resume replacement was activated")
	}
}

func TestLostUploadAckReconcilesWithoutAutomaticRetry(t *testing.T) {
	m, s, base := fake(t)
	plan, err := s.Status(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	m.failMainAck = true
	if _, err = s.Install(t.Context(), base, plan.ConfigDigest, plan.BundleDigest); err == nil {
		t.Fatal("lost ACK was reported as known success")
	}
	if m.writes != 3 || m.restarts != 0 {
		t.Fatal("unexpected retry or restart")
	}
	status, err := s.Status(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !status.DiskInstalled || !status.RestartAllowed {
		t.Fatalf("could not reconcile: %+v", status)
	}
}

func TestExistingSaveVariablesArePreserved(t *testing.T) {
	m, s, base := fake(t)
	m.settings["save_variables"] = map[string]string{"filename": "/custom/keep.cfg"}
	m.settings["pause_resume"] = map[string]any{}
	plan, err := s.Status(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(t.Context(), base, plan.ConfigDigest, plan.BundleDigest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(m.files[Filename]), "[save_variables]") || strings.Contains(string(m.files[Filename]), "[pause_resume]") {
		t.Fatal("existing configuration was redefined")
	}
}

func TestAddressRejectsCredentialsAndRedirects(t *testing.T) {
	for _, address := range []string{"file:///tmp/config", "http://user:secret@localhost:7125", "http://localhost:7125/?token=secret"} {
		if _, err := Address(address); err == nil {
			t.Fatal("unsafe address accepted")
		}
	}
	visited := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer redirect.Close()
	s := Setup{}
	if _, err := s.Status(context.Background(), redirect.URL); err == nil || visited {
		t.Fatal("followed an unapproved redirect")
	}
}
