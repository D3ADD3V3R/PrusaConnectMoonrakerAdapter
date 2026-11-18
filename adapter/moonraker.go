// Package moonraker is the dialect of a Klipper machine behind Moonraker, the
// API server of Voron, Rat Rig and most converted Enders. It is outbound: the
// server dials the address in the endpoint list. One websocket has the
// identity exchange, the subscription and every command, as JSON-RPC 2.0.
//
// The driver ships no profiles. What the machine lists under
// printer.objects.list narrows the baseline: a machine with no pause_resume
// section is not offered pause. The heater bounds are the machine's own.
//
// Klipper reports no vendor, model or serial. The endpoint options give them:
//
//	vendor    who made the machine. Empty reports "Klipper"
//	model     the model name. Empty reports the Klipper host's hostname
//	serial    the serial number. Empty reports none
//	unit_uid  the stable identity. Empty derives one from the address
//
// Set unit_uid where an address can change. The printer id is derived from it.
package moonraker

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
)

// Type is the value of a connector's `type` in configuration that selects this
// driver.
const Type connectorapi.TypeName = "pcma-moonraker"

// defaultPort is where Moonraker listens by default.
const defaultPort = "7125"

// websocketPath is where Moonraker serves its JSON-RPC API. An address that
// names a path keeps it, for a machine behind a reverse proxy.
const websocketPath = "/websocket"

const (
	// defaultTimeout bounds one call to the machine. The subscription is
	// bounded by the read deadline.
	defaultTimeout = 10 * time.Second

	// patchBacklog bounds how many state patches may wait for the host.
	patchBacklog = 64

	// maxFrameBytes bounds one websocket frame. The configuration dump is the
	// largest thing Moonraker sends.
	maxFrameBytes = 1 << 21
)

// The errors this driver reports. Callers inspect them with errors.Is.
var (
	// ErrInvalidConfig reports a config block or an address this driver cannot
	// act on.
	ErrInvalidConfig = errors.New("moonraker: invalid configuration")
	// ErrUnreachable reports a machine that did not answer.
	ErrUnreachable = errors.New("moonraker: the machine did not answer")
	// ErrRefused reports a machine that answered a call with a JSON-RPC error
	// during the identity exchange, so no session was built.
	ErrRefused = errors.New("moonraker: the machine refused a call")
)

//nolint:gochecknoinits // registration has to happen before main; the host reaches it through a blank import.
func init() { connectorapi.Register(Type, doc(), New, dialect()) }

// dialect describes the machines of this driver to a page that adds one.
//
// The core names only Prusa models, so a driver for any other machine declares
// its own model rows: a name, a vendor and a drawing. A machine joins this
// row when its endpoint option model is "Klipper".
func dialect() connectorapi.Dialect {
	return connectorapi.Dialect{
		Brands: []string{"Voron", "Rat Rig", "Creality"},
		Needs:  connectorapi.NeedsAddress,
		Models: []connectorapi.DeviceModel{{
			Model:       "Klipper",
			DisplayName: "Klipper",
			Vendor:      defaultVendor,
			Artwork:     connectorapi.Artwork{MediaType: connectorapi.ArtworkSVG, Bytes: artwork},
		}},
		States: states(),
	}
}

// artwork is the drawing of a Klipper machine.
//
//go:embed artwork.svg
var artwork []byte

// doc is what the published extension reference says about this driver.
func doc() extension.Doc {
	return extension.Doc{
		Summary: "PrusaConnectMoonrakerAdapter Moonraker files, execution receipts and local operator gates.",
		Description: "The host dials the address the endpoint list holds, asks Moonraker what the machine " +
			"is, and subscribes to the Klipper objects it reads telemetry from. What the machine reports " +
			"under printer.objects.list narrows what is offered, so a machine with no pause_resume " +
			"section is not offered pause. The endpoint options vendor, model, serial and unit_uid say " +
			"what Klipper itself does not report. Prepared starts require a persistent execution receipt, " +
			"matching compatibility profile and an acknowledged local bed confirmation. Lost start acknowledgements are reconciled.",
		Version:   "0.3.2",
		Contract:  connectorapi.Contract,
		Stability: extension.StabilityExperimental,
		Options: []extension.Option{
			{Key: "starts", Type: extension.OptionBool, Default: "false", Summary: "Enable durable prepared-execution starts with the PCMA macro contract."},
			{Key: "state_path", Type: extension.OptionString, Summary: "Persistent SQLite execution journal, required with starts."},
			{Key: "profile_id", Type: extension.OptionString, Summary: "Approved compatibility profile ID for this connector's devices."},
			{Key: "profile_version", Type: extension.OptionString, Summary: "Approved compatibility profile revision."},
			{Key: "materials", Type: extension.OptionList, Summary: "Locally approved material IDs for prepared executions."},
			{Key: "uploads", Type: extension.OptionBool, Default: "false", Summary: "Enable verified G-code uploads after probing a writable gcodes root."},
			{
				Key:     "timeout",
				Type:    extension.OptionDuration,
				Default: "10s",
				Summary: "How long one call to the machine may take before it is abandoned.",
			},
		},
	}
}

// Config is what an operator writes under a connector's `config` block.
type Config struct {
	// Timeout bounds one call to the machine, as time.ParseDuration reads a
	// duration.
	Timeout string `json:"timeout,omitempty"`
	// Uploads enables the HTTP transfer capability after a writable-root probe.
	Uploads        bool     `json:"uploads,omitempty"`
	Starts         bool     `json:"starts,omitempty"`
	StatePath      string   `json:"state_path,omitempty"`
	ProfileID      string   `json:"profile_id,omitempty"`
	ProfileVersion string   `json:"profile_version,omitempty"`
	Materials      []string `json:"materials,omitempty"`
}

// Driver shares durable execution receipts between its device sessions.
type Driver struct {
	executions *executionRuntime
	uploads    bool
	timeout    time.Duration
	live       liveness
	dialer     *websocket.Dialer
	log        *slog.Logger
}

var _ connectorapi.Outbound = (*Driver)(nil)

// New builds a Driver from its spec. It is the connectorapi.Factory for Type.
func New(spec connectorapi.Spec) (connectorapi.Driver, error) {
	var cfg Config
	if err := spec.DecodeConfig(&cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	timeout := defaultTimeout

	if cfg.Timeout != "" {
		parsed, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("%w: timeout: %w", ErrInvalidConfig, err)
		}

		if parsed <= 0 {
			return nil, fmt.Errorf("%w: timeout must be positive, and it is %s", ErrInvalidConfig, parsed)
		}

		timeout = parsed
	}

	driver := newDriver(timeout, liveness{})
	driver.uploads = cfg.Uploads
	if cfg.Starts {
		if !cfg.Uploads || cfg.ProfileID == "" || cfg.ProfileVersion == "" || len(cfg.Materials) == 0 {
			return nil, fmt.Errorf("%w: starts require uploads, profile_id, profile_version and materials", ErrInvalidConfig)
		}
		if err := validateExecutionConfig(cfg); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		store, err := openExecutions(cfg.StatePath)
		if err != nil {
			return nil, fmt.Errorf("%w: execution state: %w", ErrInvalidConfig, err)
		}
		driver.executions = &executionRuntime{store: store, profile: cfg.ProfileID, profileVersion: cfg.ProfileVersion, materials: cfg.Materials}
	}
	if spec.Logger != nil {
		driver.log = spec.Logger
	}

	return driver, nil
}

// NewDriver returns a driver with the given call timeout. Zero or less means
// the default.
func NewDriver(timeout time.Duration) *Driver { return newDriver(timeout, liveness{}) }

// newDriver also takes the liveness numbers, so a test can shorten them.
func newDriver(timeout time.Duration, live liveness) *Driver {
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	return &Driver{
		timeout: timeout,
		live:    live.orShipping(),
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
			ReadBufferSize:   readBufferBytes,
			WriteBufferSize:  writeBufferBytes,
		},
		log: slog.New(slog.DiscardHandler),
	}
}

// The websocket buffers of one connection, at the library's defaults. A
// larger frame is read in several passes.
const (
	readBufferBytes  = 4096
	writeBufferBytes = 4096
)

// Baseline is what this driver can do, for any Klipper machine. It returns a
// fresh set each time. The two temperature ceilings are the highest any
// Klipper configuration uses. Connect narrows them to the machine's own
// min_temp and max_temp.
func (d *Driver) Baseline() connectorapi.Set {
	caps := connectorapi.Set{
		connectorapi.VerbSendInfo:                                       {Flag: true},
		connectorapi.VerbSendFileInfo:                                   {Flag: true},
		connectorapi.ArgumentKey(connectorapi.VerbSendFileInfo, "path"): filePathArgument(),
		connectorapi.VerbCreateFolder:                                   {Flag: true},
		connectorapi.ArgumentKey(connectorapi.VerbCreateFolder, "path"): filePathArgument(),
		connectorapi.VerbDeleteFile:                                     {Flag: true},
		connectorapi.ArgumentKey(connectorapi.VerbDeleteFile, "path"):   filePathArgument(),
		connectorapi.VerbDeleteFolder:                                   {Flag: true},
		connectorapi.ArgumentKey(connectorapi.VerbDeleteFolder, "path"): filePathArgument(),
		VerbSendStateInfo:                                               {Flag: true},
		VerbPausePrint:                                                  {Flag: true},
		VerbResumePrint:                                                 {Flag: true},
		VerbStopPrint:                                                   {Flag: true},
		VerbNozzleTarget:                                                {Flag: true},
		ArgNozzleTarget:                                                 temperatureArgument(nozzleCeiling),
		ArgNozzleTool:                                                   toolArgumentValue(),
		VerbBedTarget:                                                   {Flag: true},
		ArgBedTarget:                                                    temperatureArgument(bedCeiling),
	}
	caps[connectorapi.VerbFileUpload] = connectorapi.Value{Flag: true}
	caps[VerbStartPrint] = connectorapi.Value{Flag: true}
	caps[connectorapi.ArgumentKey(VerbStartPrint, "path")] = filePathArgument()
	return caps
}

// Connect dials the machine, runs the identity exchange and the live probe,
// and returns a session. profile is what the host offers. The driver and the
// machine can only narrow it, so each merge is an intersection.
func (d *Driver) Connect(
	ctx context.Context, endpoint connectorapi.Endpoint, profile connectorapi.Set,
) (connectorapi.Session, error) {
	target, err := websocketURL(endpoint.Address)
	if err != nil {
		return nil, err
	}

	conn, response, err := d.dialer.DialContext(ctx, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnreachable, target, err)
	}

	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}

	conn.SetReadLimit(maxFrameBytes)

	session := newSession(conn, d.timeout, d.live, d.log)

	resolved, err := session.handshake(ctx, d.Baseline().Intersect(profile))
	if err != nil {
		session.hangUp()

		return nil, err
	}
	if d.uploads && profile.Has(connectorapi.VerbFileUpload) {
		if err := session.prepareUploads(ctx, endpoint.Address, resolved); err != nil {
			session.hangUp()
			return nil, err
		}
	}
	if d.executions != nil && profile.Has(VerbStartPrint) && session.contractVersion == 1 && len(extrudersOf(session.objects)) == 1 && slices.Contains(session.objects, "save_variables") && resolved.Has(connectorapi.VerbFileUpload) && resolved.Has(VerbStopPrint) {
		session.executions = d.executions
		carry(resolved, d.Baseline().Intersect(profile), VerbStartPrint, connectorapi.ArgumentKey(VerbStartPrint, "path"))
		carry(session.probe, resolved, VerbStartPrint, connectorapi.ArgumentKey(VerbStartPrint, "path"))
	}

	session.begin(identityOf(endpoint, session.reported), resolved)
	if session.executions != nil {
		go session.watchExecutions()
	}

	return session, nil
}

// identityOf builds the identity from the endpoint options. A missing option
// falls back to what the machine or the address gives.
func identityOf(endpoint connectorapi.Endpoint, reported printerInfo) connectorapi.Identity {
	unit := endpoint.Options[optionUnitUID]
	if unit == "" {
		unit = unitPrefix + endpoint.Address
	}

	model := endpoint.Options[optionModel]
	if model == "" {
		model = reported.Hostname
	}

	vendor := endpoint.Options[optionVendor]
	if vendor == "" {
		vendor = defaultVendor
	}

	return connectorapi.Identity{
		Vendor:   vendor,
		Model:    model,
		Firmware: connectorapi.NewFirmwareLabel(reported.SoftwareVersion),
		Serial:   endpoint.Options[optionSerial],
		UnitUID:  unit,
	}
}

// The endpoint options this dialect reads, and what it reports when one is
// absent.
const (
	optionVendor  = "vendor"
	optionModel   = "model"
	optionSerial  = "serial"
	optionUnitUID = "unit_uid"

	// defaultVendor is the vendor of a machine that names none.
	defaultVendor = "Klipper"

	// unitPrefix keeps a derived identity from colliding with one an operator
	// wrote.
	unitPrefix = "moonraker:"
)

// websocketURL turns a bare host, a host and port, or a URL into the URL
// Moonraker serves. A URL keeps its path, for a machine behind a proxy.
func websocketURL(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", fmt.Errorf("%w: the endpoint names no address", ErrInvalidConfig)
	}

	if !strings.Contains(trimmed, "://") {
		return "ws://" + withPort(trimmed) + websocketPath, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not an address: %w", ErrInvalidConfig, address, err)
	}

	switch parsed.Scheme {
	case "ws", "http":
		parsed.Scheme = "ws"
	case "wss", "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("%w: %q names the scheme %q, and this dialect speaks http, https, ws and wss",
			ErrInvalidConfig, address, parsed.Scheme)
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("%w: %q names no host", ErrInvalidConfig, address)
	}

	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = websocketPath
	}

	return parsed.String(), nil
}

// withPort adds Moonraker's default port to a host that names none.
func withPort(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}

	return net.JoinHostPort(host, defaultPort)
}

// decodeResult reads one JSON-RPC result. The error names the call.
func decodeResult(method string, result json.RawMessage, into any) error {
	if err := json.Unmarshal(result, into); err != nil {
		return fmt.Errorf("%w: the answer to %s is not readable: %w", ErrUnreachable, method, err)
	}

	return nil
}
