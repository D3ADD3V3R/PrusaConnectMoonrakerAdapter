package moonraker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi/connectorapitest"
	"pcma.local/moonraker/moonrakersim"
)

type uploadMachine struct {
	address    string
	uploads    atomic.Int64
	corrupt    bool
	queued     bool
	permission string
	block      chan struct{}
	entered    chan struct{}
}

func newMachine(t *testing.T) *uploadMachine {
	t.Helper()
	base, err := moonrakersim.Start(t.Context(), moonrakersim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	u, err := url.Parse("http://" + base.Address())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	machine := &uploadMachine{permission: "rw"}
	var stored []byte
	mux := http.NewServeMux()
	mux.Handle("/websocket", proxy)
	mux.HandleFunc("GET /server/files/roots", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"name": "gcodes", "permissions": machine.permission}}})
	})
	mux.HandleFunc("POST /server/files/upload", func(w http.ResponseWriter, r *http.Request) {
		machine.uploads.Add(1)
		if machine.entered != nil {
			close(machine.entered)
		}
		if machine.block != nil {
			select {
			case <-machine.block:
			case <-r.Context().Done():
				return
			}
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		stored, err = io.ReadAll(file)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.FormValue("root") != "gcodes" || r.FormValue("print") != "false" {
			t.Error("upload changed root or requested a print")
		}
		if r.FormValue("checksum") != fmt.Sprintf("%x", sha256.Sum256(stored)) {
			t.Error("checksum missing or incorrect")
		}
		item := map[string]any{"root": "gcodes", "path": path.Join(r.FormValue("path"), header.Filename), "size": len(stored)}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"item": item, "print_started": false, "print_queued": machine.queued})
	})
	mux.HandleFunc("GET /server/files/gcodes/", func(w http.ResponseWriter, r *http.Request) {
		data := bytes.Clone(stored)
		if machine.corrupt && len(data) > 0 {
			data[0] ^= 1
		}
		_, _ = w.Write(data)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	machine.address = server.URL
	return machine
}

func connectMachine(t *testing.T, machine *uploadMachine, profile connectorapi.Set) *Session {
	t.Helper()
	driver, err := New(connectorapi.Spec{Config: json.RawMessage(`{"uploads":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if profile == nil {
		profile = driver.Baseline()
	}
	session, err := driver.(connectorapi.Outbound).Connect(t.Context(), connectorapi.Endpoint{Address: machine.address}, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session.(*Session)
}

func TestPCMAContract(t *testing.T) {
	connectorapitest.RunDriverContract(t, func(t *testing.T) connectorapitest.Subject {
		driver, err := New(connectorapi.Spec{Config: json.RawMessage(`{"uploads":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		return connectorapitest.Subject{
			Driver:      driver,
			Endpoint:    connectorapi.Endpoint{Address: newMachine(t).address},
			Connectable: true, SafeVerb: "core.command.send_state_info",
		}
	})
}

type content struct {
	text     string
	declared int64
}

func (c content) Size() int64 { return c.declared }
func (c content) Open(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(c.text[offset:])), nil
}

func upload(text string) connectorapi.Upload {
	return connectorapi.Upload{Path: "gcodes/pcma/job-1.gcode", Content: content{text, int64(len(text))}}
}

func TestTransferVerifiesDestination(t *testing.T) {
	for _, name := range []string{"nested", "root", "empty", "corrupt", "queued"} {
		t.Run(name, func(t *testing.T) {
			machine := newMachine(t)
			machine.corrupt = name == "corrupt"
			machine.queued = name == "queued"
			session := connectMachine(t, machine, nil)
			up := upload("; test file\n")
			if name == "empty" {
				up = upload("")
			}
			if name == "root" {
				up.Path = "gcodes/part with spaces.gcode"
			}
			var final connectorapi.Progress
			up.Progress = func(p connectorapi.Progress) { final = p }
			err := session.Send(t.Context(), up)
			if machine.corrupt || machine.queued {
				if !errors.Is(err, connectorapi.ErrTransferFailed) {
					t.Fatalf("expected verification failure, got %v", err)
				}
				if final.Known {
					t.Fatal("failed transfer reported confirmed bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !final.Known || final.Received != up.Content.Size() {
				t.Fatalf("unconfirmed transfer: %+v", final)
			}
		})
	}
}

func TestTransferRefusesInvalidInputBeforeSending(t *testing.T) {
	machine := newMachine(t)
	session := connectMachine(t, machine, nil)
	for _, value := range []string{"config/printer.cfg", "gcodes/../evil.gcode", "gcodes/a/../../evil.gcode", "gcodes/a\\evil.gcode", "gcodes/%2e%2e/a.gcode", "gcodes//evil.gcode", "gcodes/.gcode/../evil.gcode", "gcodes/a.bgcode"} {
		up := upload("G28\n")
		up.Path = value
		if err := session.Send(t.Context(), up); !errors.Is(err, connectorapi.ErrTransferRefused) {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, length := range []int64{0, 2, 20} {
		up := upload("G28\n")
		up.Content = content{"G28\n", length}
		if err := session.Send(t.Context(), up); !errors.Is(err, connectorapi.ErrTransferRefused) {
			t.Errorf("length %d: %v", length, err)
		}
	}
	if got := machine.uploads.Load(); got != 0 {
		t.Fatalf("sent %d invalid uploads", got)
	}
}

func TestProfileAndDeviceCanRemoveUploads(t *testing.T) {
	for _, reason := range []string{"profile", "device"} {
		t.Run(reason, func(t *testing.T) {
			machine := newMachine(t)
			driver, err := New(connectorapi.Spec{Config: json.RawMessage(`{"uploads":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			profile := driver.Baseline()
			if reason == "profile" {
				delete(profile, connectorapi.VerbFileUpload)
			} else {
				machine.permission = "r"
			}
			session := connectMachine(t, machine, profile)
			caps, _ := session.Capabilities()
			if caps.Has(connectorapi.VerbFileUpload) {
				t.Fatal("upload survived capability narrowing")
			}
			if err := session.Send(t.Context(), upload("G28\n")); !errors.Is(err, connectorapi.ErrNoTransfers) {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentUploadAndClose(t *testing.T) {
	machine := newMachine(t)
	machine.block = make(chan struct{})
	machine.entered = make(chan struct{})
	defer close(machine.block)
	session := connectMachine(t, machine, nil)
	done := make(chan error, 1)
	go func() { done <- session.Send(t.Context(), upload("; data\n")) }()
	select {
	case <-machine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach server")
	}
	if err := session.Send(t.Context(), upload("; second\n")); !errors.Is(err, connectorapi.ErrTransferBusy) {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed session completed a transfer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not cancel upload")
	}
	if err := session.Send(t.Context(), upload("; after close\n")); err == nil {
		t.Fatal("closed session accepted upload")
	}
}
