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
	if err := os.MkdirAll(filepath.Join(dir, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test", "bundler.yaml"), []byte("schema: 1\nname: bundler\ntitle: B\ndetect: 'true'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadSaved(t, cfgPath)
	cfg.UpsertPreset(config.PresetRef{Name: "broken"})
	cfg.UpsertPreset(config.PresetRef{Name: "future"})
	cfg.UpsertPreset(config.PresetRef{Name: "gone"})
	cfg.UpsertPreset(config.PresetRef{Name: "test/fetched", Ref: "3f2c1ab", SHA256: "9a41"})
	cfg.UpsertPreset(config.PresetRef{Name: "test/bundler", Ref: "3f2c1ab", SHA256: "9a41"})

	presets, warnings := loadEnabled(cfg, cfgPath)
	if len(presets) != 2 || presets[0].Key() != "stack" || presets[1].Key() != "test/bundler" {
		t.Fatalf("presets = %+v", presets)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		`preset "broken" skipped: broken.yaml: title is required`,
		`preset "future" skipped: future.yaml: schema 9`,
		"upgrade nearhost",
		`preset "gone" is enabled but ` + filepath.Join(dir, "gone.yaml") + " is missing; put the file back or run `nearhost preset disable gone`",
		`preset "test/fetched" is enabled but ` + filepath.Join(dir, "test", "fetched.yaml") + " is missing; run `nearhost preset enable test/fetched` to install it again",
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
	if got := presetPath("/tmp/elsewhere/config.yaml", "acme/stack"); got != filepath.Join("/tmp/elsewhere", "presets", "acme", "stack.yaml") {
		t.Errorf("presetPath of a registry preset = %q", got)
	}
	if got := registryDir("/tmp/elsewhere/config.yaml", "acme"); got != filepath.Join("/tmp/elsewhere", "registry", "acme") {
		t.Errorf("registryDir = %q", got)
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

// A preset kept as a link to a file the user manages is written through: the
// link stays and its file gets the new contents.
func TestWriteFileAtomicWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "mine.yaml")
	if err := os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "preset.yaml")
	if err := os.Symlink("mine.yaml", link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	if data, err := os.ReadFile(real); err != nil || string(data) != "new\n" {
		t.Fatalf("linked file = %q, %v", data, err)
	}
}
