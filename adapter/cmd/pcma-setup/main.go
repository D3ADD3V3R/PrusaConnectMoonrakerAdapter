// Command pcma-setup serves the PCMA macro installer through the host's module API.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
	"pcma.local/moonraker/macroconfig"
)

//go:embed openapi.json
var openAPI []byte

type settings struct {
	Addresses string `json:"addresses"`
}
type fleet interface {
	Get(context.Context, deviceapi.GetParams) (deviceapi.Device, error)
}
type access interface {
	Check(context.Context, accessapi.CheckParams) (accessapi.Decision, error)
}
type server struct {
	fleet   fleet
	access  access
	setup   macroconfig.Setup
	mu      sync.RWMutex
	targets map[string]map[string]bool
}

func answer(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, code string) {
	answer(w, status, map[string]string{"code": code})
}

func (s *server) target(w http.ResponseWriter, r *http.Request, configure bool) (string, bool) {
	principal := routesapi.PrincipalOf(r)
	if principal == "" {
		failure(w, http.StatusUnauthorized, "PCMA_CALLER_REQUIRED")
		return "", false
	}
	id := r.PathValue("id")
	d, err := s.fleet.Get(r.Context(), deviceapi.GetParams{Principal: principal, ID: id})
	if err != nil || !d.Connected || d.Retired {
		failure(w, http.StatusNotFound, "PCMA_DEVICE_UNAVAILABLE")
		return "", false
	}
	if configure {
		decision, err := s.access.Check(r.Context(), accessapi.CheckParams{Subject: principal, Action: "configure", ObjectType: "device", ObjectID: id})
		if err != nil || !decision.Allowed {
			failure(w, http.StatusForbidden, "PCMA_CONFIGURE_REQUIRED")
			return "", false
		}
	}
	var raw string
	if json.Unmarshal(d.State["/pcma/moonraker_address"], &raw) != nil {
		failure(w, http.StatusConflict, "PCMA_ADAPTER_UPDATE_REQUIRED")
		return "", false
	}
	base, err := macroconfig.Address(raw)
	s.mu.RLock()
	allowed := s.targets[d.Driver][base]
	s.mu.RUnlock()
	if err != nil || !allowed {
		failure(w, http.StatusConflict, "PCMA_TARGET_NOT_CONFIGURED")
		return "", false
	}
	return base, true
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices/{id}/macros", func(w http.ResponseWriter, r *http.Request) {
		base, ok := s.target(w, r, false)
		if !ok {
			return
		}
		status, err := s.setup.Status(r.Context(), base)
		if err != nil {
			failure(w, http.StatusBadGateway, "PCMA_PROBE_FAILED")
			return
		}
		decision, permissionErr := s.access.Check(r.Context(), accessapi.CheckParams{Subject: routesapi.PrincipalOf(r), Action: "configure", ObjectType: "device", ObjectID: r.PathValue("id")})
		if permissionErr != nil || !decision.Allowed {
			status.Installable = false
			status.RestartAllowed = false
			if !status.Ready {
				status.Reason = "configure_required"
			}
		}
		answer(w, http.StatusOK, status)
	})
	mux.HandleFunc("POST /devices/{id}/macros", func(w http.ResponseWriter, r *http.Request) {
		base, ok := s.target(w, r, true)
		if !ok {
			return
		}
		var plan struct {
			Config string `json:"config_digest"`
			Bundle string `json:"bundle_digest"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || len(plan.Config) != 64 || len(plan.Bundle) != 64 {
			failure(w, http.StatusBadRequest, "PCMA_PLAN_REQUIRED")
			return
		}
		status, err := s.setup.Install(r.Context(), base, plan.Config, plan.Bundle)
		if err != nil {
			answer(w, http.StatusConflict, map[string]any{"code": "PCMA_INSTALL_UNCERTAIN", "status": status})
			return
		}
		answer(w, http.StatusOK, status)
	})
	mux.HandleFunc("POST /devices/{id}/macros/restart", func(w http.ResponseWriter, r *http.Request) {
		base, ok := s.target(w, r, true)
		if !ok {
			return
		}
		var plan struct {
			Config string `json:"config_digest"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&plan) != nil || len(plan.Config) != 64 {
			failure(w, http.StatusBadRequest, "PCMA_PLAN_REQUIRED")
			return
		}
		if err := s.setup.Restart(r.Context(), base, plan.Config); err != nil {
			failure(w, http.StatusConflict, "PCMA_RESTART_UNCERTAIN")
			return
		}
		answer(w, http.StatusOK, map[string]bool{"restart_requested": true})
	})
	return mux
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return nil }

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	line, err := (wire.Handshake{Protocol: wire.ProtocolMajor, MinMinor: wire.MinMinor, MaxMinor: wire.MaxMinor, Address: wire.StdioAddress}).Line()
	if err != nil {
		return err
	}
	if _, err = os.Stdout.WriteString(line); err != nil {
		return err
	}
	methods := wirerpc.Methods{}
	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{Role: wirerpc.RoleChild, Methods: methods})
	if err != nil {
		return err
	}
	s := &server{fleet: deviceapi.NewClient(conn), access: accessapi.NewClient(conn)}
	maps.Copy(methods, wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(wirerpc.Info{Name: "pcma-setup", Version: extension.Version("0.3.5"), Contract: wirerpc.Contract, Serves: []wirerpc.Method{wirerpc.MethodConfigure}}, openAPI)
		},
		wirerpc.MethodConfigure: wirerpc.ConfigureReporting(func(_ context.Context, config settings, _ wirerpc.Grants) (wirerpc.Configured, error) {
			var addresses map[string][]string
			if err := json.Unmarshal([]byte(config.Addresses), &addresses); err != nil || len(addresses) == 0 {
				return wirerpc.Configured{}, fmt.Errorf("configure at least one Moonraker address")
			}
			allowed := map[string]map[string]bool{}
			for driver, targets := range addresses {
				if driver == "" || len(targets) == 0 {
					return wirerpc.Configured{}, fmt.Errorf("each connector requires an approved address")
				}
				allowed[driver] = map[string]bool{}
				for _, raw := range targets {
					base, err := macroconfig.Address(raw)
					if err != nil {
						return wirerpc.Configured{}, err
					}
					allowed[driver][base] = true
				}
			}
			s.mu.Lock()
			s.targets = allowed
			s.mu.Unlock()
			return wirerpc.Configured{}, nil
		}),
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) { return nil, nil },
		routesapi.MethodServe:  routesapi.Serve(s.routes()),
	})
	return conn.Serve(ctx)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
