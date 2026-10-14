package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func TestLoadEnabledSkipsWhatItCannotUse(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	enablePresets(t, cfgPath, "stack")
	dir := presetsDir(cfgPath)
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("schema: 1\nname: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "future.yaml"), []byte("schema: 9\nname: future\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadSaved(t, cfgPath)
	cfg.UpsertPreset(config.PresetRef{Name: "broken"})
	cfg.UpsertPreset(config.PresetRef{Name: "future"})
	cfg.UpsertPreset(config.PresetRef{Name: "gone"})
	cfg.UpsertPreset(config.PresetRef{Name: "fetched", Ref: "3f2c1ab"})

	presets, warnings := loadEnabled(cfg, cfgPath)
	if len(presets) != 1 || presets[0].Name != "stack" {
		t.Fatalf("presets = %+v", presets)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		`preset "broken" skipped: broken.yaml: title is required`,
		`preset "future" skipped: future.yaml: schema 9`,
		"upgrade nearhost",
		`preset "gone" is enabled but ` + filepath.Join(dir, "gone.yaml") + " is missing; put the file back or run `nearhost preset disable gone`",
		`preset "fetched" is enabled but ` + filepath.Join(dir, "fetched.yaml") + " is missing; run `nearhost preset enable fetched` to install it again",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	if len(warnings) != 4 {
		t.Errorf("expected four warnings, got %d:\n%s", len(warnings), joined)
	}
}

func TestPresetsLiveBesideTheConfigInUse(t *testing.T) {
	if got := presetPath("/tmp/elsewhere/config.yaml", "stack"); got != filepath.Join("/tmp/elsewhere", "presets", "stack.yaml") {
		t.Errorf("presetPath = %q", got)
	}
}

func TestPrintable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Test stack", "Test stack"},
		{"Café Ünïcode 日本語", "Café Ünïcode 日本語"},
		{"a\nb", "a b"},
		{"a\tb", "a b"},
		{"a\rb", "a b"},
		{"\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"  padded \n", "padded"},
	}
	for _, tc := range cases {
		if got := printable(tc.in); got != tc.want {
			t.Errorf("printable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
