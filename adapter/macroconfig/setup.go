package macroconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var Required = []string{"PCMA_STATE", "PCMA_FILAMENT_CHANGE", "PCMA_LOAD_FILAMENT", "PCMA_UNLOAD_FILAMENT", "PCMA_LOCAL_LOAD", "PCMA_PURGE_FILAMENT", "PCMA_CONFIRM_FILAMENT", "PCMA_RESET_FILAMENT"}

const Filename = "pcma-filament.cfg"
const maxConfig = 1 << 20

// Setup only writes the compiled bundle and one include, never arbitrary G-code.
type Setup struct {
	Client *http.Client
	mu     sync.Mutex
}

type Status struct {
	Missing        []string `json:"missing"`
	Ready          bool     `json:"ready"`
	Installable    bool     `json:"installable"`
	Reason         string   `json:"reason"`
	DiskInstalled  bool     `json:"disk_installed"`
	RestartAllowed bool     `json:"restart_allowed"`
	ConfigDigest   string   `json:"config_digest"`
	BundleDigest   string   `json:"bundle_digest"`
	Backup         string   `json:"backup,omitempty"`
}

type snapshot struct {
	status   Status
	main     string
	original []byte
	bundle   []byte
	query    map[string]map[string]json.RawMessage
}

func Digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// Address refuses credentials, queries and fragments. Targets must also be
// matched against the operator's allowlist by the caller before reaching Setup.
func Address(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	bare := !strings.Contains(raw, "://")
	if bare {
		if net.ParseIP(raw) != nil {
			raw = net.JoinHostPort(raw, "7125")
		}
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err == nil && u != nil {
		switch u.Scheme {
		case "ws":
			u.Scheme = "http"
		case "wss":
			u.Scheme = "https"
		}
		if bare && u.Host != "" && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "7125")
		}
		u.Path = strings.TrimSuffix(u.Path, "/websocket")
	}
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid Moonraker HTTP address")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func (s *Setup) request(ctx context.Context, base, method, suffix string, data io.Reader, contentType string) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, method, base+suffix, data)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *c
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copyClient.Do(r)
	if err != nil {
		return nil, fmt.Errorf("Moonraker request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("Moonraker returned HTTP %d", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxConfig+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxConfig {
		return nil, fmt.Errorf("Moonraker response exceeds 1 MiB")
	}
	return b, nil
}

func (s *Setup) json(ctx context.Context, base, suffix string, into any) error {
	b, err := s.request(ctx, base, http.MethodGet, suffix, nil, "")
	if err != nil {
		return err
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal(b, &reply); err != nil {
		return err
	}
	return json.Unmarshal(reply.Result, into)
}

func text(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func fileRoute(name string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/server/files/config/" + strings.Join(parts, "/")
}
func (s *Setup) file(ctx context.Context, base, name string) ([]byte, error) {
	return s.request(ctx, base, http.MethodGet, fileRoute(name), nil, "")
}

func idle(q map[string]map[string]json.RawMessage) bool {
	state := text(q["print_stats"]["state"])
	var paused bool
	_ = json.Unmarshal(q["pause_resume"]["is_paused"], &paused)
	return !paused && (state == "" || slices.Contains([]string{"standby", "complete", "cancelled"}, state)) && slices.Contains([]string{"Ready", "Idle"}, text(q["idle_timeout"]["state"]))
}

func includeLine(name string) string { return "[include " + name + "]" }
func hasInclude(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == includeLine(Filename) {
			return true
		}
	}
	return false
}
func addInclude(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	index := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "#*#") && strings.Contains(line, "SAVE_CONFIG") {
			index = i
			break
		}
	}
	before := strings.TrimRight(strings.Join(lines[:index], "\n"), "\r\n")
	after := strings.Join(lines[index:], "\n")
	return []byte(before + "\n\n" + includeLine(Filename) + "\n\n" + after)
}

func variant(saveVariables, pauseResume bool) []byte {
	b := bytes.Clone(Filament)
	if saveVariables {
		b = bytes.Replace(b, []byte("[save_variables]\nfilename: ~/pcma-variables.cfg\n"), nil, 1)
	}
	if pauseResume {
		b = bytes.Replace(b, []byte("[pause_resume]\n"), nil, 1)
	}
	return b
}
func knownVariant(data []byte) bool {
	for _, save := range []bool{false, true} {
		for _, pause := range []bool{false, true} {
			if bytes.Equal(data, variant(save, pause)) {
				return true
			}
		}
	}
	return false
}

func (s *Setup) inspect(ctx context.Context, base string) (snapshot, error) {
	var x snapshot
	x.status.Missing = []string{}
	var list struct {
		Objects []string `json:"objects"`
	}
	if err := s.json(ctx, base, "/printer/objects/list", &list); err != nil {
		return x, err
	}
	for _, name := range Required {
		if !slices.Contains(list.Objects, "gcode_macro "+name) {
			x.status.Missing = append(x.status.Missing, name)
		}
	}
	var q struct {
		Status map[string]map[string]json.RawMessage `json:"status"`
	}
	query := "/printer/objects/query?configfile&idle_timeout"
	for _, object := range []string{"print_stats", "pause_resume", "gcode_macro PCMA_STATE"} {
		if slices.Contains(list.Objects, object) {
			query += "&" + url.QueryEscape(object)
		}
	}
	if err := s.json(ctx, base, query, &q); err != nil {
		return x, err
	}
	x.query = q.Status
	if slices.Contains(list.Objects, "print_stats") && text(q.Status["print_stats"]["state"]) == "" {
		x.status.Reason = "printer_not_idle"
		return x, nil
	}
	if slices.Contains(list.Objects, "pause_resume") {
		var paused bool
		if json.Unmarshal(q.Status["pause_resume"]["is_paused"], &paused) != nil {
			x.status.Reason = "printer_not_idle"
			return x, nil
		}
	}
	var version int
	_ = json.Unmarshal(q.Status["gcode_macro PCMA_STATE"]["filament_contract_version"], &version)
	x.status.Ready = len(x.status.Missing) == 0 && version == 1
	var roots []struct{ Name, Path, Permissions string }
	if err := s.json(ctx, base, "/server/files/roots", &roots); err != nil {
		return x, err
	}
	root := ""
	for _, r := range roots {
		if r.Name == "config" && r.Permissions == "rw" {
			root = r.Path
		}
	}
	if root == "" {
		x.status.Reason = "config_read_only"
		return x, nil
	}
	var info struct {
		ConfigFile string `json:"config_file"`
	}
	if err := s.json(ctx, base, "/printer/info", &info); err != nil {
		return x, err
	}
	relative, err := filepath.Rel(root, info.ConfigFile)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || strings.ContainsAny(relative, "\\\r\n") {
		x.status.Reason = "main_config_outside_root"
		return x, nil
	}
	x.main = filepath.ToSlash(relative)
	if path.Ext(x.main) != ".cfg" || strings.ContainsAny(x.main, "[]/") || x.main == Filename {
		x.status.Reason = "unsupported_main_config"
		return x, nil
	}
	x.original, err = s.file(ctx, base, x.main)
	if err != nil {
		return x, err
	}
	x.status.ConfigDigest = Digest(x.original)
	x.bundle = bytes.Clone(Filament)
	settings := rawObject(q.Status["configfile"]["settings"])
	if _, ok := settings["extruder"]; !ok {
		x.status.Reason = "requires_single_extruder"
		return x, nil
	}
	for name := range settings {
		if strings.HasPrefix(name, "extruder") && name != "extruder" && name != "extruder_stepper" && len(name) > 8 && name[8] >= '0' && name[8] <= '9' {
			x.status.Reason = "requires_single_extruder"
			return x, nil
		}
	}
	if _, exists := settings["save_variables"]; exists {
		x.bundle = bytes.Replace(x.bundle, []byte("[save_variables]\nfilename: ~/pcma-variables.cfg\n"), nil, 1)
	}
	if _, exists := settings["pause_resume"]; exists {
		x.bundle = bytes.Replace(x.bundle, []byte("[pause_resume]\n"), nil, 1)
	}
	x.status.BundleDigest = Digest(x.bundle)
	var files []struct {
		Path string `json:"path"`
	}
	if err = s.json(ctx, base, "/server/files/list?root=config", &files); err != nil {
		return x, err
	}
	for _, f := range files {
		if f.Path != Filename {
			continue
		}
		installed, e := s.file(ctx, base, Filename)
		if e != nil {
			return x, e
		}
		// Both the fresh and adapted forms are ours; preserve an installed form.
		if !bytes.Equal(installed, x.bundle) && !(hasInclude(x.original) && knownVariant(installed)) {
			x.status.Reason = "macro_file_conflict"
			return x, nil
		}
		x.bundle = installed
		x.status.BundleDigest = Digest(installed)
		x.status.DiskInstalled = hasInclude(x.original)
	}
	if !idle(x.query) {
		x.status.Reason = "printer_not_idle"
		return x, nil
	}
	if _, custom := settings["gcode_macro resume"]; custom {
		actual := text(rawObject(settings["gcode_macro resume"])["gcode"])
		if !x.status.Ready || normalizeGcode(actual) != normalizeGcode(resumeTemplate()) {
			x.status.Reason = "custom_resume_requires_merge"
			return x, nil
		}
	}
	if !x.status.Ready {
		allNames := append([]string(nil), list.Objects...)
		for name := range settings {
			allNames = append(allNames, name)
		}
		for _, name := range allNames {
			upper := strings.ToUpper(name)
			if strings.HasPrefix(upper, "GCODE_MACRO PCMA_") || strings.HasPrefix(upper, "GCODE_MACRO _PCMA_") || strings.HasPrefix(upper, "DELAYED_GCODE _PCMA_") || strings.HasPrefix(upper, "DELAYED_GCODE PCMA_") {
				x.status.Reason = "existing_pcma_requires_review"
				return x, nil
			}
		}
	}
	if x.status.DiskInstalled {
		x.status.RestartAllowed = true
		return x, nil
	}
	x.status.Installable = true
	return x, nil
}

func normalizeGcode(code string) string {
	var out []string
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
func resumeTemplate() string {
	section := strings.Split(string(Filament), "[gcode_macro RESUME]\n")
	if len(section) != 2 {
		return ""
	}
	_, body, ok := strings.Cut(section[1], "gcode:\n")
	if !ok {
		return ""
	}
	return body
}

func (s *Setup) Status(ctx context.Context, base string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.inspect(ctx, base)
	return x.status, err
}

func (s *Setup) upload(ctx context.Context, base, filename string, data []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("root", "config"); err != nil {
		return err
	}
	if err := w.WriteField("print", "false"); err != nil {
		return err
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	_, err = s.request(ctx, base, http.MethodPost, "/server/files/upload", &buf, w.FormDataContentType())
	return err
}

// Install never restarts Klipper. HTTP file APIs have no cross-client CAS;
// digests reject observed changes, and the backup remains available on errors.
func (s *Setup) Install(ctx context.Context, base, configDigest, bundleDigest string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.inspect(ctx, base)
	if err != nil {
		return x.status, err
	}
	if configDigest != x.status.ConfigDigest || bundleDigest != x.status.BundleDigest {
		return x.status, fmt.Errorf("configuration changed; refresh the installation plan")
	}
	if x.status.DiskInstalled {
		return x.status, nil
	}
	if !x.status.Installable {
		return x.status, fmt.Errorf("macro installation is unavailable: %s", x.status.Reason)
	}
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		return x.status, err
	}
	backup := x.main + ".pcma-backup-" + hex.EncodeToString(token[:])
	copyBody, _ := json.Marshal(map[string]any{"source": "config/" + x.main, "dest": "config/" + backup})
	if _, err = s.request(ctx, base, http.MethodPost, "/server/files/copy", bytes.NewReader(copyBody), "application/json"); err != nil {
		return x.status, err
	}
	x.status.Backup = backup
	if b, e := s.file(ctx, base, backup); e != nil || !bytes.Equal(b, x.original) {
		return x.status, fmt.Errorf("could not verify configuration backup")
	}
	if err = s.upload(ctx, base, Filename, x.bundle); err != nil {
		return x.status, fmt.Errorf("macro upload has an uncertain outcome; refresh before continuing: %w", err)
	}
	if b, e := s.file(ctx, base, Filename); e != nil || !bytes.Equal(b, x.bundle) {
		return x.status, fmt.Errorf("macro upload could not be verified")
	}
	current, err := s.inspect(ctx, base)
	if err != nil {
		return x.status, err
	}
	if !current.status.Installable || !bytes.Equal(current.original, x.original) || !idle(current.query) {
		return x.status, fmt.Errorf("configuration or printer state changed; include was not written")
	}
	modified := addInclude(x.original)
	if err = s.upload(ctx, base, x.main, modified); err != nil {
		return x.status, fmt.Errorf("include upload has an uncertain outcome; refresh before continuing: %w", err)
	}
	if b, e := s.file(ctx, base, x.main); e != nil || !bytes.Equal(b, modified) {
		return x.status, fmt.Errorf("include upload could not be verified")
	}
	x.status.ConfigDigest = Digest(modified)
	x.status.DiskInstalled, x.status.RestartAllowed, x.status.Installable = true, true, false
	return x.status, nil
}

func (s *Setup) Restart(ctx context.Context, base, configDigest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.inspect(ctx, base)
	if err != nil {
		return err
	}
	if !x.status.RestartAllowed || x.status.ConfigDigest != configDigest {
		return fmt.Errorf("refresh the plan and verify an idle printer before restart")
	}
	_, err = s.request(ctx, base, http.MethodPost, "/printer/restart", nil, "")
	return err
}
