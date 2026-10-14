package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

const (
	askStack   = "This looks like a Test stack project. Enable the stack preset? [Y/n] "
	askBundler = "This looks like a Test bundler project. Enable the bundler preset? [Y/n] "
)

// suggesting is an add backend with a fake registry and a terminal, or not.
func suggesting(t *testing.T, terminal bool, names ...string) (*fakeRegistry, addBackend) {
	t.Helper()
	f, presets := newFakeRegistry(t, refOne, names...)
	b := noRemote(t)
	b.presets = presets
	b.interactive = func() bool { return terminal }
	return f, b
}

func TestAddOffersMatchingPresetsAndUsesThem(t *testing.T) {
	for name, input := range map[string]string{"typed y": "y\nYES\n", "pressed return": "\n\n"} {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			_, b := suggesting(t, true, "stack", "bundler")
			out, err := executeWithInput(t, input, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askStack, askBundler, "  8080  web       App (APP_PORT)\n", "  5174  loopback  Assets (VITE_PORT)\n", "Advisory (app-url)", "Added myapp on local")
			if strings.Count(out, askStack) != 1 || strings.Count(out, askBundler) != 1 {
				t.Errorf("each preset should be offered once:\n%s", out)
			}
			cfg := loadSaved(t, cfgPath)
			for _, name := range []string{"stack", "bundler"} {
				if ref, ok := cfg.Preset(name); !ok || ref.Ref != refOne {
					t.Errorf("%s = %+v %v", name, ref, ok)
				}
				if _, err := os.Stat(presetPath(cfgPath, name)); err != nil {
					t.Errorf("%s was not installed: %v", name, err)
				}
			}
			if _, ok := cfg.Project("myapp"); !ok {
				t.Error("the project was not saved")
			}
		})
	}
}

func TestAddDeclinedPresetsAreNotUsed(t *testing.T) {
	for name, input := range map[string]string{"typed n": "n\nno\n", "no answer at all": ""} {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			f, b := suggesting(t, true, "stack", "bundler")
			out, err := executeWithInput(t, input, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askStack, askBundler, "  8080  web     laravel.test (APP_PORT)\n", "Added myapp on local")
			if strings.Count(out, askStack) != 1 {
				t.Errorf("a declined preset must not be offered again in the same run:\n%s", out)
			}
			if cfg := loadSaved(t, cfgPath); len(cfg.Presets) != 0 {
				t.Errorf("presets = %+v", cfg.Presets)
			}
			if f.hits["/"+refOne+"/presets/stack.yaml"] != 0 {
				t.Error("a declined preset must not be downloaded")
			}
		})
	}
}

// A project no registry preset matches is asked nothing.
func TestAddAsksOnlyAboutPresetsThatMatch(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, b := suggesting(t, true, "stack", "bundler")
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "plain")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Enable the") {
		t.Errorf("no preset matches this project, so nothing should be offered:\n%s", out)
	}
}

func TestAddWithoutATerminalOnlyPrintsSuggestions(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := suggesting(t, false, "stack", "bundler")
	// Input is waiting, and must not be read as an answer.
	out, err := executeWithInput(t, "y\ny\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out,
		"This looks like a Test stack project. Pass `--preset stack` to `nearhost add` to use its preset.\n",
		"This looks like a Test bundler project. Pass `--preset bundler` to `nearhost add` to use its preset.\n",
		"  8080  web     laravel.test (APP_PORT)\n",
		"Added myapp on local",
	)
	if strings.Contains(out, "[Y/n]") {
		t.Errorf("nothing may be asked without a terminal:\n%s", out)
	}
	if cfg := loadSaved(t, cfgPath); len(cfg.Presets) != 0 {
		t.Errorf("presets = %+v", cfg.Presets)
	}
	if _, err := os.Stat(presetsDir(cfgPath)); err == nil {
		t.Error("nothing may be installed without a terminal")
	}
	if f.hits["/main/index.yaml"] != 1 || f.total() != 1 {
		t.Errorf("only the index should be fetched: %v", f.hits)
	}
}

func TestAddPresetFlagEnablesWithoutAsking(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, b := suggesting(t, false, "stack", "bundler")
	out, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--preset", "stack", "--preset", "bundler")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  8080  web       App (APP_PORT)\n")
	if strings.Contains(out, "This looks like") {
		t.Errorf("an enabled preset should not be suggested:\n%s", out)
	}
	if cfg := loadSaved(t, cfgPath); len(cfg.Presets) != 2 {
		t.Errorf("presets = %+v", cfg.Presets)
	}
}

func TestAddPresetFlagFailsForAnUnknownPreset(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, b := suggesting(t, false, "stack")
	_, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--preset", "nope")
	if err == nil || !strings.Contains(err.Error(), `no preset named "nope"`) {
		t.Fatalf("expected an unknown preset error, got %v", err)
	}
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Error("nothing may be saved")
	}
}

func TestAddNoPresetsRunsCoreDiscoveryOnly(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := suggesting(t, true, "stack", "bundler")
	enablePresets(t, cfgPath, "stack")
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  8080  web     laravel.test (APP_PORT)\n")
	if strings.Contains(out, "Enable the") || strings.Contains(out, "This looks like") || f.total() != 0 {
		t.Errorf("--no-presets should neither suggest nor contact the registry (%v):\n%s", f.hits, out)
	}

	if _, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "x", "--no-presets", "--preset", "stack"); err == nil || !strings.Contains(err.Error(), "--no-presets") {
		t.Errorf("the two flags contradict each other, got %v", err)
	}
}

func TestAddMakesNoRequestWhenTheRegistryIsOff(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := suggesting(t, true, "stack")
	off := false
	saveConfig(t, cfgPath, &config.Config{Registry: &off})
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if f.total() != 0 || strings.Contains(out, "Enable the") || strings.Contains(out, "Warning") {
		t.Errorf("registry: false should be silent and make no request (%v):\n%s", f.hits, out)
	}
}

func TestAddSaysWhenSuggestionsAreUnavailable(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := suggesting(t, true, "stack")
	f.down = true
	out, err := executeWithInput(t, "", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("the registry being down must not stop add: %v\n%s", err, out)
	}
	wantContains(t, out, "Warning: preset suggestions are unavailable because the registry could not be reached.\n", "Added myapp on local")
	if strings.Count(out, "Warning:") != 1 {
		t.Errorf("expected one line about it:\n%s", out)
	}
}

// A preset that cannot be installed is reported, and the project is added
// with what discovery knows without it.
func TestAddCarriesOnWhenAnAcceptedPresetCannotBeInstalled(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := suggesting(t, true, "stack")
	f.files["/"+refOne+"/presets/stack.yaml"] += "\n# tampered\n"
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "Warning: the stack preset was not enabled:", "does not match the registry index", "  8080  web     laravel.test (APP_PORT)\n", "Added myapp on local")
	if strings.Count(out, askStack) != 1 {
		t.Errorf("a preset that failed to install must not be offered again:\n%s", out)
	}
}

func TestAddHelpNamesThePresetFlags(t *testing.T) {
	out, err := execute(t, newAddCmd(noRemote(t)), "add", "--help")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "--preset stringArray", "--no-presets")
}

// Offer titles come from the registry index, which is untrusted.
func TestAddMakesSuggestedTitlesSafeToPrint(t *testing.T) {
	for name, terminal := range map[string]bool{"with a terminal": true, "without a terminal": false} {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			f, b := suggesting(t, terminal, "stack")
			f.files["/main/index.yaml"] = strings.Replace(f.files["/main/index.yaml"], "title: Test stack", `title: "Evil\n  forged line\e[31mred"`, 1)
			out, err := executeWithInput(t, "n\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			if strings.Contains(out, "\x1b") {
				t.Errorf("output carries an escape character: %q", out)
			}
			wantContains(t, out, "This looks like a Evil   forged line[31mred project. ")
		})
	}
}
