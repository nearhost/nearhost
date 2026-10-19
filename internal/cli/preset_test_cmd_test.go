package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPresetsDir() string {
	return filepath.Join("..", "discovery", "testdata", "presets")
}

func TestPresetTestPassesGoodFixtures(t *testing.T) {
	out, err := execute(t, newPresetCmd(presetBackend{}), "preset", "test", filepath.Join("testdata", "fixtures"), "--presets", testPresetsDir())
	if err != nil {
		t.Fatalf("preset test: %v\n%s", err, out)
	}
	if out != "ok    pass\n1 fixture passed.\n" {
		t.Errorf("printed %q", out)
	}
}

func TestPresetTestReportsEveryFailure(t *testing.T) {
	out, err := execute(t, newPresetCmd(presetBackend{}), "preset", "test", filepath.Join("testdata", "fixtures-bad"), "--presets", testPresetsDir())
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
	_, err := execute(t, newPresetCmd(presetBackend{}), "preset", "test", t.TempDir(), "--presets", testPresetsDir())
	if err == nil || !strings.Contains(err.Error(), "no fixtures") {
		t.Fatalf("expected an error about no fixtures, got %v", err)
	}
}

// A fixture directory without expected.yaml, as when the file is misnamed,
// fails rather than being skipped. Hidden directories are not fixtures.
func TestPresetTestFailsAFixtureWithNoExpectedYAML(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"typo", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "typo", "expected.yml"), []byte("presets: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newPresetCmd(presetBackend{}), "preset", "test", dir, "--presets", testPresetsDir())
	if err == nil || !strings.Contains(err.Error(), "1 of 1 fixtures failed") {
		t.Fatalf("expected the fixture to fail, got %v\n%s", err, out)
	}
	if out != "FAIL  typo\n      no expected.yaml\n" {
		t.Errorf("printed %q", out)
	}
}

// Without --presets the fixtures use the presets directory beside the config.
func TestPresetTestDefaultsToTheInstalledPresets(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	putPreset(t, cfgPath, "stack")
	out, err := execute(t, newPresetCmd(presetBackend{}), "--config", cfgPath, "preset", "test", filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("preset test: %v\n%s", err, out)
	}
}

// A route's hosts are compared with the fixture's, so a fixture that leaves
// them out fails.
func TestPresetTestComparesHosts(t *testing.T) {
	out, err := execute(t, newPresetCmd(presetBackend{}), "preset", "test", filepath.Join("testdata", "fixtures-hosts"), "--presets", filepath.Join("testdata", "presets-hosts"))
	if err == nil || !strings.Contains(err.Error(), "1 of 2 fixtures failed") {
		t.Fatalf("expected one failure, got %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  missing\n",
		"        want  8220 web App\n",
		"        got   8220 web App hosts=app.local.x.io\n",
		"ok    hosted\n",
	)
}
