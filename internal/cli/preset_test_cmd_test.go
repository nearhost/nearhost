package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func testPresetsDir() string {
	return filepath.Join("..", "discovery", "testdata", "presets")
}

func TestPresetTestPassesGoodFixtures(t *testing.T) {
	out, err := execute(t, newPresetCmd(), "preset", "test", filepath.Join("testdata", "fixtures"), "--presets", testPresetsDir())
	if err != nil {
		t.Fatalf("preset test: %v\n%s", err, out)
	}
	if out != "ok    pass\n1 fixture passed.\n" {
		t.Errorf("printed %q", out)
	}
}

func TestPresetTestReportsEveryFailure(t *testing.T) {
	out, err := execute(t, newPresetCmd(), "preset", "test", filepath.Join("testdata", "fixtures-bad"), "--presets", testPresetsDir())
	if err == nil || !strings.Contains(err.Error(), "2 of 2 fixtures failed") {
		t.Fatalf("expected both fixtures to fail, got %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  unknown-preset\n",
		"nope.yaml",
		"FAIL  wrong\n",
		"      routes\n",
		"        want  8080 web Not the label\n",
		"        got   8080 web app\n",
		"      advisories\n",
		"        want  app-url\n",
		"        got   (none)\n",
	)
}

func TestPresetTestNeedsFixtures(t *testing.T) {
	_, err := execute(t, newPresetCmd(), "preset", "test", t.TempDir(), "--presets", testPresetsDir())
	if err == nil || !strings.Contains(err.Error(), "no fixtures") {
		t.Fatalf("expected an error about no fixtures, got %v", err)
	}
}

// Without --presets the fixtures use the presets directory beside the config.
func TestPresetTestDefaultsToTheInstalledPresets(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	putPreset(t, cfgPath, "stack")
	out, err := execute(t, newPresetCmd(), "--config", cfgPath, "preset", "test", filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("preset test: %v\n%s", err, out)
	}
}
