// Package gcode prepares immutable PCMA execution artifacts from approved G-code.
package gcode

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const manifestPrefix = "; PCMA_MANIFEST "

// Manifest binds the derived file to one execution and one approved profile revision.
type Manifest struct {
	Version        int    `json:"version"`
	ExecutionID    string `json:"execution_id"`
	ProfileID      string `json:"profile_id"`
	ProfileVersion string `json:"profile_version"`
	OriginalSHA256 string `json:"original_sha256"`
	Material       string `json:"material"`
	Color          string `json:"color"`
}

// Artifact describes the bytes stored in the prepared file.
type Artifact struct {
	Manifest
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Options supplies the approved execution context. Empty material/color uses slicer metadata.
type Options struct {
	ExecutionID    string
	ProfileID      string
	ProfileVersion string
	Material       string
	Color          string
}

// SourceMetadata reads supported slicer values without changing the source.
func SourceMetadata(src io.ReadSeeker) (material, color string, err error) {
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	result, err := scan(src)
	return result.material, result.color, err
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var profileID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var materialID = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,31}$`)
var colorID = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,31}$`)

func normalizeColor(value string) (string, error) {
	value = strings.ToUpper(strings.Trim(value, "\""))
	if strings.HasPrefix(value, "#") {
		if len(value) != 7 {
			return "", fmt.Errorf("color needs six hex digits")
		}
		if _, err := hex.DecodeString(value[1:]); err != nil {
			return "", fmt.Errorf("invalid filament color")
		}
		value = "C_" + value[1:]
	}
	return value, nil
}

// Validate checks values that may become executable macro arguments.
func (m Manifest) Validate() error {
	if m.Version != 1 || !identifier.MatchString(m.ExecutionID) || !profileID.MatchString(m.ProfileID) || !profileID.MatchString(m.ProfileVersion) || !materialID.MatchString(m.Material) || !colorID.MatchString(m.Color) {
		return fmt.Errorf("invalid PCMA execution metadata")
	}
	decoded, err := hex.DecodeString(m.OriginalSHA256)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid original SHA-256")
	}
	return nil
}

// Hook is the early material gate executed by Klipper for this artifact.
func (m Manifest) Hook() string {
	return fmt.Sprintf("PCMA_PRINT_START EXECUTION=%s MATERIAL=%s COLOR=%s", m.ExecutionID, m.Material, m.Color)
}

func matchesHook(command string, manifest Manifest) bool {
	fields := strings.Fields(command)
	if len(fields) != 4 || !strings.EqualFold(fields[0], "PCMA_PRINT_START") {
		return false
	}
	expected := map[string]string{"EXECUTION": manifest.ExecutionID, "MATERIAL": manifest.Material, "COLOR": manifest.Color}
	for _, field := range fields[1:] {
		key, value, found := strings.Cut(field, "=")
		key = strings.ToUpper(key)
		if !found || expected[key] != value || value == "" {
			return false
		}
		delete(expected, key)
	}
	return len(expected) == 0
}

type scanResult struct {
	manifest            *Manifest
	material            string
	color               string
	firstCommand        string
	hooks               int
	hash                string
	size                int64
	originalWithHook    string
	originalWithoutHook string
}

func splitLinesWithEnding(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func scan(src io.Reader) (scanResult, error) {
	var result scanResult
	hash := sha256.New()
	withHook, withoutHook := sha256.New(), sha256.New()
	count := &countReader{reader: io.TeeReader(src, hash)}
	scanner := bufio.NewScanner(count)
	scanner.Split(splitLinesWithEnding)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if strings.ContainsRune(scanner.Text(), '\ufeff') {
			return result, fmt.Errorf("UTF-8 byte order marks require source normalization before approval")
		}
		line := strings.TrimSpace(scanner.Text())
		manifestLine := strings.HasPrefix(line, manifestPrefix)
		if !manifestLine {
			_, _ = withHook.Write(scanner.Bytes())
		}
		commandLine := strings.TrimSpace(strings.SplitN(line, ";", 2)[0])
		isHook := commandLine != "" && strings.EqualFold(strings.Fields(commandLine)[0], "PCMA_PRINT_START")
		if !manifestLine && !isHook {
			_, _ = withoutHook.Write(scanner.Bytes())
		}
		if strings.HasPrefix(line, manifestPrefix) {
			if result.manifest != nil || result.firstCommand != "" {
				return result, fmt.Errorf("duplicate or late PCMA manifest")
			}
			var manifest Manifest
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, manifestPrefix)), &manifest); err != nil {
				return result, err
			}
			if err := manifest.Validate(); err != nil {
				return result, err
			}
			result.manifest = &manifest
		}
		if strings.HasPrefix(line, ";") {
			key, value, found := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, ";")), "=")
			if !found {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			switch key {
			case "filament_type":
				value = strings.ToUpper(strings.Trim(value, "\""))
				if !materialID.MatchString(value) || result.material != "" && result.material != value {
					return result, fmt.Errorf("ambiguous or multi-material filament_type")
				}
				result.material = value
			case "filament_colour", "filament_color":
				var err error
				value, err = normalizeColor(value)
				if err != nil {
					return result, err
				}
				if !colorID.MatchString(value) || result.color != "" && result.color != value {
					return result, fmt.Errorf("ambiguous or unsupported filament color")
				}
				result.color = value
			}
			continue
		}
		command := strings.TrimSpace(strings.SplitN(line, ";", 2)[0])
		if command == "" {
			continue
		}
		if result.firstCommand == "" {
			result.firstCommand = command
		}
		word := strings.ToUpper(strings.Fields(command)[0])
		if word == "PCMA_PRINT_START" {
			result.hooks++
		}
		toolChange := false
		if strings.HasPrefix(word, "T") {
			tool, err := strconv.Atoi(strings.TrimPrefix(word, "T"))
			toolChange = err == nil && tool != 0
		}
		if word == "M600" || toolChange {
			return result, fmt.Errorf("file requires an unsupported in-print material/tool change")
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read G-code: %w", err)
	}
	result.hash = hex.EncodeToString(hash.Sum(nil))
	result.size = count.size
	result.originalWithHook = hex.EncodeToString(withHook.Sum(nil))
	result.originalWithoutHook = hex.EncodeToString(withoutHook.Sum(nil))
	return result, nil
}

// Inspect requires exactly one matching hook as the first executable command.
func Inspect(src io.Reader) (Artifact, error) {
	result, err := scan(src)
	if err != nil {
		return Artifact{}, err
	}
	if result.manifest == nil || result.hooks != 1 || !matchesHook(result.firstCommand, *result.manifest) {
		return Artifact{}, fmt.Errorf("file needs one matching PCMA hook before all executable G-code")
	}
	if result.material != "" && result.material != result.manifest.Material {
		return Artifact{}, fmt.Errorf("manifest material conflicts with slicer metadata")
	}
	if !originalHashMatches(result) {
		return Artifact{}, fmt.Errorf("manifest original hash differs from the retained source bytes")
	}
	return Artifact{Manifest: *result.manifest, SHA256: result.hash, Size: result.size}, nil
}

// Prepare writes a derived artifact while leaving the source unchanged.
func Prepare(src io.ReadSeeker, dst io.Writer, options Options) (Artifact, error) {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return Artifact{}, err
	}
	result, err := scan(src)
	if err != nil {
		return Artifact{}, err
	}
	material := strings.ToUpper(options.Material)
	if material == "" {
		material = result.material
	}
	color, err := normalizeColor(options.Color)
	if err != nil {
		return Artifact{}, err
	}
	if color == "" {
		color = result.color
	}
	if color == "" {
		color = "UNKNOWN"
	}
	if options.Material != "" && result.material != "" && material != result.material {
		return Artifact{}, fmt.Errorf("requested material conflicts with slicer metadata")
	}
	manifest := Manifest{Version: 1, ExecutionID: options.ExecutionID, ProfileID: options.ProfileID, ProfileVersion: options.ProfileVersion, OriginalSHA256: result.hash, Material: material, Color: color}
	if result.manifest != nil {
		if !originalHashMatches(result) {
			return Artifact{}, fmt.Errorf("existing manifest has an invalid original hash")
		}
		manifest.OriginalSHA256 = result.manifest.OriginalSHA256
		if options.Material == "" {
			manifest.Material = result.manifest.Material
		}
		if options.Color == "" {
			manifest.Color = result.manifest.Color
		}
		if *result.manifest != manifest || result.hooks != 1 || !matchesHook(result.firstCommand, manifest) {
			return Artifact{}, fmt.Errorf("existing PCMA hook belongs to a different execution context")
		}
	} else if result.hooks != 0 && (result.hooks != 1 || !matchesHook(result.firstCommand, manifest)) {
		return Artifact{}, fmt.Errorf("existing PCMA hook conflicts with the approved execution context")
	}
	if err := manifest.Validate(); err != nil {
		return Artifact{}, err
	}
	if result.firstCommand == "" {
		return Artifact{}, fmt.Errorf("G-code contains no executable commands")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return Artifact{}, err
	}
	hash := sha256.New()
	out := io.MultiWriter(dst, hash)
	var size int64
	if result.manifest == nil {
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return Artifact{}, err
		}
		n, err := fmt.Fprintf(out, "%s%s\n", manifestPrefix, encoded)
		if err != nil {
			return Artifact{}, err
		}
		size = int64(n)
		if result.hooks == 0 {
			n, err := fmt.Fprintln(out, manifest.Hook())
			if err != nil {
				return Artifact{}, err
			}
			size += int64(n)
		}
	}
	copyHash := sha256.New()
	n, err := io.Copy(out, io.TeeReader(src, copyHash))
	if err != nil {
		return Artifact{}, err
	}
	if hex.EncodeToString(copyHash.Sum(nil)) != result.hash || n != result.size {
		return Artifact{}, fmt.Errorf("source changed during artifact preparation")
	}
	return Artifact{Manifest: manifest, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size + n}, nil
}

type countReader struct {
	reader io.Reader
	size   int64
}

func originalHashMatches(result scanResult) bool {
	return strings.EqualFold(result.manifest.OriginalSHA256, result.originalWithHook) || strings.EqualFold(result.manifest.OriginalSHA256, result.originalWithoutHook)
}

func (r *countReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.size += int64(n)
	return n, err
}
