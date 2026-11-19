package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

func TestPublishedOpenAPIIsAccepted(t *testing.T) {
	if _, err := routesapi.Describe(wirerpc.Info{Name: "pcma-setup", Version: extension.Version("0.3.5"), Contract: wirerpc.Contract, Serves: []wirerpc.Method{wirerpc.MethodConfigure}}, openAPI); err != nil {
		t.Fatal(err)
	}
}

type fakeFleet struct {
	address       string
	principal, id string
	denied        bool
	driver        string
}

func (f *fakeFleet) Get(_ context.Context, p deviceapi.GetParams) (deviceapi.Device, error) {
	f.principal, f.id = p.Principal, p.ID
	if f.denied {
		return deviceapi.Device{}, errors.New("denied")
	}
	address, _ := json.Marshal(f.address)
	driver := f.driver
	if driver == "" {
		driver = "pcma"
	}
	return deviceapi.Device{Driver: driver, Connected: true, State: map[string]json.RawMessage{"/pcma/moonraker_address": address}}, nil
}

type fakeAccess struct {
	allowed bool
	last    accessapi.CheckParams
}

func (f *fakeAccess) Check(_ context.Context, p accessapi.CheckParams) (accessapi.Decision, error) {
	f.last = p
	return accessapi.Decision{Allowed: f.allowed}, nil
}

func TestConfigurationWritesRequireCallerPermissionAndApprovedTarget(t *testing.T) {
	for _, which := range []string{"no_caller", "no_configure", "unapproved_target", "foreign_driver", "no_observe"} {
		t.Run(which, func(t *testing.T) {
			fleet := &fakeFleet{address: "http://127.0.0.1:9"}
			access := &fakeAccess{allowed: true}
			principal := "user:alice"
			s := &server{fleet: fleet, access: access, targets: map[string]map[string]bool{"pcma": {fleet.address: true}}}
			want := http.StatusForbidden
			switch which {
			case "no_caller":
				principal = ""
				want = http.StatusUnauthorized
			case "no_configure":
				access.allowed = false
			case "unapproved_target":
				s.targets = map[string]map[string]bool{}
				want = http.StatusConflict
			case "foreign_driver":
				fleet.driver = "unrelated"
				want = http.StatusConflict
			case "no_observe":
				fleet.denied = true
				want = http.StatusNotFound
			}
			r, err := (routesapi.Request{Method: http.MethodPost, Path: "/devices/printer-1/macros", Principal: principal, Body: []byte(`{"config_digest":"` + strings.Repeat("a", 64) + `","bundle_digest":"` + strings.Repeat("b", 64) + `"}`)}).HTTP(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// A spoofed header cannot replace the host-owned request principal.
			r.Header.Set("X-Principal", "user:admin")
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("got%d want%d: %s", w.Code, want, w.Body.String())
			}
			if which != "no_caller" && (fleet.principal != "user:alice" || fleet.id != "printer-1") {
				t.Fatal("fleet lookup did not preserve caller/device")
			}
			if which == "no_configure" && (access.last.Action != "configure" || access.last.Subject != "user:alice" || access.last.ObjectID != "printer-1") {
				t.Fatal("wrong permission check")
			}
		})
	}
}
