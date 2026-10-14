package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// putPreset writes a test preset into the presets directory without
// enabling it.
func putPreset(t *testing.T, cfgPath, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "discovery", "testdata", "presets", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(presetsDir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presetPath(cfgPath, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPresetEnableAndDisableALocalFile(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	putPreset(t, cfgPath, "stack")

	out, err := execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "enable", "stack")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	if out != "Enabled preset stack (Test stack).\n" {
		t.Errorf("enable printed %q", out)
	}
	if p, ok := loadSaved(t, cfgPath).Preset("stack"); !ok || p.Ref != "" {
		t.Fatalf("preset = %+v %v", p, ok)
	}

	out, err = execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "enable", "stack")
	if err != nil || out != "Preset stack is already enabled.\n" {
		t.Errorf("second enable: %q %v", out, err)
	}

	out, err = execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "disable", "stack")
	if err != nil {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	if out != "Disabled preset stack. Its file is kept at "+presetPath(cfgPath, "stack")+".\n" {
		t.Errorf("disable printed %q", out)
	}
	if _, ok := loadSaved(t, cfgPath).Preset("stack"); ok {
		t.Error("stack is still enabled")
	}
	if _, err := os.Stat(presetPath(cfgPath, "stack")); err != nil {
		t.Errorf("disable removed the file: %v", err)
	}

	if _, err := execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "disable", "stack"); err == nil || !strings.Contains(err.Error(), `preset "stack" is not enabled`) {
		t.Errorf("second disable: %v", err)
	}
}

func TestPresetEnableRefusesWhatItCannotUse(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.MkdirAll(presetsDir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presetPath(cfgPath, "broken"), []byte("schema: 1\nname: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		want []string
	}{
		{"missing", []string{`no preset named "missing"`, presetPath(cfgPath, "missing")}},
		{"broken", []string{"broken.yaml", "title is required", "detect is required"}},
		{"../etc", []string{"lowercase letters, digits and hyphens"}},
	}
	for _, tc := range cases {
		_, err := execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "enable", tc.name)
		if err == nil {
			t.Fatalf("enable %s succeeded", tc.name)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("enable %s: error %q lacks %q", tc.name, err, want)
			}
		}
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Error("a refused enable must not write the config")
	}
}

func TestPresetList(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "list")
	if err != nil || out != "No presets are enabled.\n" {
		t.Fatalf("empty list: %q %v", out, err)
	}

	enablePresets(t, cfgPath, "stack", "bundler")
	cfg := loadSaved(t, cfgPath)
	cfg.UpsertPreset(config.PresetRef{Name: "bundler", Ref: "3f2c1ab9d0e4", SHA256: "9a41"})
	cfg.UpsertPreset(config.PresetRef{Name: "gone"})
	saveConfig(t, cfgPath, cfg)

	out, err = execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	wantContains(t, out,
		"Enabled presets:\n",
		"  NAME     SOURCE            TITLE\n",
		"  stack    local             Test stack\n",
		"  bundler  registry 3f2c1ab  Test bundler\n",
		"  gone     local             (cannot be used)\n",
		`Warning: preset "gone" is enabled but`,
	)
}

func TestPresetValidate(t *testing.T) {
	good := filepath.Join("..", "discovery", "testdata", "presets", "stack.yaml")
	out, err := execute(t, newPresetCmd(presetBackend{}), "preset", "validate", good)
	if err != nil || out != good+" is valid.\n" {
		t.Fatalf("validate: %q %v", out, err)
	}

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("schema: 1\nname: other\ndetect: 'file('\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, newPresetCmd(presetBackend{}), "preset", "validate", bad)
	if err == nil {
		t.Fatal("validate accepted a bad preset")
	}
	for _, want := range []string{`must match the file name "bad"`, "title is required", "detect:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	if _, err := execute(t, newPresetCmd(presetBackend{}), "preset", "validate", filepath.Join(dir, "notes.txt")); err == nil || !strings.Contains(err.Error(), ".yaml") {
		t.Errorf("expected an error about the extension, got %v", err)
	}
	if _, err := execute(t, newPresetCmd(presetBackend{}), "preset", "validate", filepath.Join(dir, "nope.yaml")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
