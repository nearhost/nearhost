package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

const (
	askRegistry = "No preset registries are trusted, so Nearhost will read ports from .env.\n" +
		"You can also set routes yourself in the config.\n" +
		"Trust the official presets (nearhost/presets) for framework-specific setups?\n" +
		"[Y]es / [n]o / [d]on't ask again: "
	askRegistryAgain = "[Y]es / [n]o / [d]on't ask again: "
	askOfficialStack = "This looks like a Test stack project. Enable nearhost/stack from nearhost/presets? [Y/n] "
)

var officialReg = config.Registry{Alias: "nearhost", Repo: "nearhost/presets"}

// asking is an add backend with no registry trusted, whose fake official
// registry offers the stack preset, with a terminal or not.
func asking(t *testing.T, terminal bool) (string, *fakeRegistries, addBackend) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, presets := newFakeRegistries(t)
	f.publish(officialReg.Repo, refOne, "stack")
	b := noRemote(t)
	b.presets = presets
	b.interactive = func() bool { return terminal }
	return cfgPath, f, b
}

func addSailCustom(t *testing.T, b addBackend, cfgPath, input, name string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", name}, extra...)
	return executeWithInput(t, input, newAddCmd(b), args...)
}

func TestAddTrustsTheOfficialRegistryWhenTheAnswerIsYes(t *testing.T) {
	for name, input := range map[string]string{"pressed return": "\n\n", "typed y": "y\ny\n", "typed YES": " YES \ny\n"} {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := asking(t, true)
			out, err := addSailCustom(t, b, cfgPath, input, "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askRegistry, "Trusted registry nearhost (nearhost/presets). Its index at "+refOne[:7]+" offers 1 preset.\n",
				askOfficialStack, "  8080  web       App (APP_PORT)\n", "Added myapp on local")
			if strings.Contains(out, "No preset registries are trusted. To use") {
				t.Errorf("the hint repeats the question:\n%s", out)
			}
			cfg := loadSaved(t, cfgPath)
			if len(cfg.Registries) != 1 || cfg.Registries[0] != officialReg {
				t.Errorf("registries = %+v", cfg.Registries)
			}
			if _, ok := cfg.Preset("nearhost/stack"); !ok {
				t.Errorf("presets = %+v", cfg.Presets)
			}
			if f.hit("/nearhost/presets/HEAD/index.yaml") != 1 {
				t.Errorf("the index should be fetched once: %v", f.hits)
			}
		})
	}
}

func TestAddAsksAgainNextTimeWhenTheAnswerIsNo(t *testing.T) {
	for name, input := range map[string]string{"typed n": "n\n", "typed No": "No\n", "no answer at all": ""} {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := asking(t, true)
			out, err := addSailCustom(t, b, cfgPath, input, "one", "--web", "8080")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askRegistry, "  8080  web     app (APP_PORT)\n", "Added one on local")
			cfg := loadSaved(t, cfgPath)
			if len(cfg.Registries) != 0 || cfg.RegistryPromptOff() {
				t.Errorf("a no saves nothing: %+v", cfg)
			}
			if f.total() != 0 {
				t.Errorf("a no makes no request: %v", f.hits)
			}
			out, _ = addSailCustom(t, b, cfgPath, "n\n", "two", "--web", "8080")
			wantContains(t, out, askRegistry)
		})
	}
}

func TestAddNeverAsksAgainAfterDontAskAgain(t *testing.T) {
	for name, input := range map[string]string{"typed d": "d\n", "typed don't": "don't\n", "typed DONT": "DONT\n"} {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := asking(t, true)
			out, err := addSailCustom(t, b, cfgPath, input, "one", "--web", "8080")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askRegistry, "Added one on local")
			if !loadSaved(t, cfgPath).RegistryPromptOff() {
				t.Error("registry_prompt: false was not saved")
			}
			for _, terminal := range []bool{true, false} {
				b.interactive = func() bool { return terminal }
				name := map[bool]string{true: "two", false: "three"}[terminal]
				out, err := addSailCustom(t, b, cfgPath, "y\n", name, "--web", "8080")
				if err != nil {
					t.Fatalf("add: %v\n%s", err, out)
				}
				if strings.Contains(out, "Trust the official presets") || strings.Contains(out, "No preset registries") {
					t.Errorf("terminal %v: asked or hinted after don't ask again:\n%s", terminal, out)
				}
			}
			if f.total() != 0 {
				t.Errorf("no request may be made: %v", f.hits)
			}
		})
	}
}

func TestAddAsksAgainOnAnAnswerItDoesNotKnow(t *testing.T) {
	cfgPath, _, b := asking(t, true)
	out, err := addSailCustom(t, b, cfgPath, "maybe\nyep\nn\n", "myapp", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if n := strings.Count(out, askRegistryAgain); n != 3 {
		t.Errorf("asked %d times, want 3:\n%s", n, out)
	}
	if strings.Count(out, "Trust the official presets") != 1 {
		t.Errorf("the question should be shown once:\n%s", out)
	}
	if cfg := loadSaved(t, cfgPath); len(cfg.Registries) != 0 || cfg.RegistryPromptOff() {
		t.Errorf("config = %+v", cfg)
	}
}

// A yes whose index cannot be read saves nothing, warns once and carries on
// with .env.
func TestAddCarriesOnWhenTheOfficialRegistryCannotBeRead(t *testing.T) {
	cfgPath, f, b := asking(t, true)
	f.down[officialReg.Repo] = true
	out, err := addSailCustom(t, b, cfgPath, "y\n", "myapp", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, askRegistry, "Warning: registry nearhost (nearhost/presets) cannot be read: ", ", so it was not trusted.\n", "  8080  web     app (APP_PORT)\n", "Added myapp on local")
	if strings.Count(out, "Warning:") != 1 {
		t.Errorf("expected one warning:\n%s", out)
	}
	if strings.Contains(out, "nothing was saved") {
		t.Errorf("add saves the project, so the note must not say nothing was saved:\n%s", out)
	}
	if cfg := loadSaved(t, cfgPath); len(cfg.Registries) != 0 || cfg.RegistryPromptOff() {
		t.Errorf("nothing may be saved but the project: %+v", cfg)
	}
}

func TestAddDoesNotAskAboutRegistriesWhenItShouldNot(t *testing.T) {
	cases := map[string]struct {
		terminal bool
		setup    func(t *testing.T, cfgPath string)
		extra    []string
		hint     bool
	}{
		"without a terminal":      {terminal: false, hint: true},
		"with --no-presets":       {terminal: true, extra: []string{"--no-presets"}},
		"with --preset":           {terminal: true, setup: func(t *testing.T, cfgPath string) { enablePresets(t, cfgPath, "stack") }, extra: []string{"--preset", "stack"}},
		"with a registry trusted": {terminal: true, setup: func(t *testing.T, cfgPath string) { trust(t, cfgPath, testReg) }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := asking(t, tc.terminal)
			saveConfig(t, cfgPath, &config.Config{})
			if tc.setup != nil {
				tc.setup(t, cfgPath)
			}
			out, err := addSailCustom(t, b, cfgPath, "y\n", "myapp", append(tc.extra, "--web", "8080")...)
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			if strings.Contains(out, "Trust the official presets") {
				t.Errorf("asked:\n%s", out)
			}
			if got := strings.Contains(out, noRegistriesHint); got != tc.hint {
				t.Errorf("hint shown = %v, want %v:\n%s", got, tc.hint, out)
			}
			if f.requests(officialReg.Repo) != 0 {
				t.Errorf("the official registry was asked: %v", f.hits)
			}
		})
	}
}

// A config that cannot be saved stops add before discovery when the answer
// is don't ask again.
func TestAddFailsWhenDontAskAgainCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	cfgPath, _, b := asking(t, true)
	saveConfig(t, cfgPath, &config.Config{})
	dir := filepath.Dir(cfgPath)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	out, err := addSailCustom(t, b, cfgPath, "d\n", "myapp", "--web", "8080")
	if err == nil || !strings.Contains(err.Error(), "create temp config") {
		t.Fatalf("expected the save error, got %v\n%s", err, out)
	}
	if strings.Contains(out, "Routes found") {
		t.Errorf("discovery ran:\n%s", out)
	}
}
