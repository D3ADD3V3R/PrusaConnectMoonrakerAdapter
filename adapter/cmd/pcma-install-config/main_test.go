package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestInstallerProcess(t *testing.T) {
	if os.Getenv("PCMA_CONFIG_HELPER_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			flag.CommandLine = flag.NewFlagSet("config-test", flag.ExitOnError)
			main()
			os.Exit(0)
		}
	}
}

func invoke(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	c := exec.Command(os.Args[0], append([]string{"-test.run=^TestInstallerProcess$", "--"}, args...)...)
	c.Env = append(os.Environ(), "PCMA_CONFIG_HELPER_TEST=1")
	return c.CombinedOutput()
}

func TestRejectUnsafeConfiguration(t *testing.T) {
	for name, text := range map[string]string{
		"large":       "core: {}\n#" + strings.Repeat("a", 8<<20) + "\nresources: {}\n",
		"duplicate":   "core: {}\ncore: {}\n",
		"alias":       "core: &shared {}\nother: *shared\n",
		"documents":   "core: {}\n---\nresources: {}\n",
		"not mapping": "[core]\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := invoke(t, "-config", path); err == nil {
				t.Fatalf("accepted unsafe input: %s", result)
			}
		})
	}
}

func TestMergePreservesExistingSettingsAndComments(t *testing.T) {
	dir := t.TempDir()
	config, planPath, result := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "plan.json"), filepath.Join(dir, "result.yaml")
	text := "# Keep this comment\ncore:\n  overlay: custom/resources.json\n  auth:\n    setup_code: keep-secret\n  connectors:\n    existing:\n      type: previous-moonraker\n      config:\n        starts: false\n        uploads: true\n        state_path: keep.db\nresources:\n  storage:\n    existing:\n      type: filesystem\n      config: {root: ./storage}\n"
	if err := os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	asset := map[string]any{"type": "com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/pcma-tools", "enabled": true, "required": false, "asset": map[string]any{"path": "/modules/pcma", "checksum": "sha256:test"}}
	p, err := json.Marshal(plan{Profiles: map[string]string{"existing": "/profiles/current.json", "pcma": "/profiles/current.json"}, Asset: asset})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(planPath, p, 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := invoke(t, "-config", config, "-plan", planPath, "-output", result); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	b, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# Keep this comment") {
		t.Fatal("comment lost")
	}
	var merged map[string]any
	if err = yaml.Unmarshal(b, &merged); err != nil {
		t.Fatal(err)
	}
	core := merged["core"].(map[string]any)
	if core["auth"].(map[string]any)["setup_code"] != "keep-secret" {
		t.Fatal("unrelated auth changed")
	}
	cs := core["connectors"].(map[string]any)
	c := cs["existing"].(map[string]any)
	settings := c["config"].(map[string]any)
	if c["type"] != "pcma-moonraker" || settings["starts"] != false || settings["uploads"] != true || settings["state_path"] != "keep.db" {
		t.Fatalf("settings lost: %v", settings)
	}
	if _, ok := merged["resources"].(map[string]any)["storage"]; !ok {
		t.Fatal("storage lost")
	}
	if original, _ := os.ReadFile(config); string(original) != text {
		t.Fatal("live input modified")
	}
	if b, err := invoke(t, "-config", result, "-work", dir); err != nil || !strings.Contains(string(b), "custom") {
		t.Fatalf("inspect: %s %v", b, err)
	}
}

func TestRetainsImplicitShippingConnector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("core: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := invoke(t, "-config", path)
	if err != nil {
		t.Fatalf("%s: %v", result, err)
	}
	var info struct {
		Connectors map[string]map[string]any `json:"connectors"`
	}
	if err = json.Unmarshal(result, &info); err != nil {
		t.Fatal(err)
	}
	if info.Connectors["xbuddy"]["type"] != "xbuddy" {
		t.Fatal("implicit shipping connector lost")
	}
}
