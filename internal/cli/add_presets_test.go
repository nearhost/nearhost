package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	askStack   = "This looks like a Test stack project. Enable test/stack from test/presets? [Y/n] "
	askBundler = "This looks like a Test bundler project. Enable test/bundler from test/presets? [Y/n] "
)

// suggesting is an add backend whose config trusts the test registry, which
// offers the named presets, with a terminal or not.
func suggesting(t *testing.T, terminal bool, names ...string) (string, *fakeRegistries, addBackend) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, presets := newFakeRegistries(t)
	f.publish(testReg.Repo, refOne, names...)
	trust(t, cfgPath, testReg)
	b := noRemote(t)
	b.presets = presets
	b.interactive = func() bool { return terminal }
	return cfgPath, f, b
}

func TestAddOffersMatchingPresetsAndUsesThem(t *testing.T) {
	for name, input := range map[string]string{"typed y": "y\nYES\n", "pressed return": "\n\n"} {
		t.Run(name, func(t *testing.T) {
			cfgPath, _, b := suggesting(t, true, "stack", "bundler")
			out, err := executeWithInput(t, input, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, askStack, askBundler, "  8080  web       App (APP_PORT)\n", "  5174  loopback  Assets (VITE_PORT)\n", "Advisory (app-url)", "Added myapp on local")
			if strings.Count(out, askStack) != 1 || strings.Count(out, askBundler) != 1 {
				t.Errorf("each preset should be offered once:\n%s", out)
			}
			cfg := loadSaved(t, cfgPath)
			for _, name := range []string{"test/stack", "test/bundler"} {
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
			cfgPath, f, b := suggesting(t, true, "stack", "bundler")
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
			if f.hit("/test/presets/"+refOne+"/presets/stack.yaml") != 0 {
				t.Error("a declined preset must not be downloaded")
			}
		})
	}
}

// A project no registry preset matches is asked nothing.
func TestAddAsksOnlyAboutPresetsThatMatch(t *testing.T) {
	cfgPath, _, b := suggesting(t, true, "stack", "bundler")
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "plain")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "[Y/n]") {
		t.Errorf("no preset matches this project, so nothing should be offered:\n%s", out)
	}
}

func TestAddWithoutATerminalOnlyPrintsSuggestions(t *testing.T) {
	cfgPath, f, b := suggesting(t, false, "stack", "bundler")
	// Input is waiting, and must not be read as an answer.
	out, err := executeWithInput(t, "y\ny\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out,
		"This looks like a Test stack project. Pass `--preset test/stack` to `nearhost add` to use its preset.\n",
		"This looks like a Test bundler project. Pass `--preset test/bundler` to `nearhost add` to use its preset.\n",
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
	if f.hit("/test/presets/HEAD/index.yaml") != 1 || f.total() != 1 {
		t.Errorf("only the index should be fetched: %v", f.hits)
	}
}

func TestAddPresetFlagEnablesWithoutAsking(t *testing.T) {
	cfgPath, _, b := suggesting(t, false, "stack", "bundler")
	out, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--preset", "test/stack", "--preset", "test/bundler")
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
	for _, name := range []string{"test/nope", "nope", "other/stack"} {
		cfgPath, _, b := suggesting(t, false, "stack")
		_, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--preset", name)
		if err == nil || !strings.Contains(err.Error(), "no preset named") && !strings.Contains(err.Error(), "No registry is trusted") {
			t.Fatalf("--preset %s: expected an unknown preset error, got %v", name, err)
		}
		if _, ok := loadSaved(t, cfgPath).Project("myapp"); ok {
			t.Error("nothing may be saved")
		}
	}
}

func TestAddNoPresetsRunsCoreDiscoveryOnly(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack", "bundler")
	enablePresets(t, cfgPath, "stack")
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  8080  web     laravel.test (APP_PORT)\n")
	if strings.Contains(out, "[Y/n]") || strings.Contains(out, "This looks like") || f.total() != 0 {
		t.Errorf("--no-presets should neither suggest nor contact a registry (%v):\n%s", f.hits, out)
	}

	if _, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "x", "--no-presets", "--preset", "stack"); err == nil || !strings.Contains(err.Error(), "--no-presets") {
		t.Errorf("the two flags contradict each other, got %v", err)
	}
}

func TestAddSaysWhenARegistryCannotBeRead(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack")
	f.down[testReg.Repo] = true
	out, err := executeWithInput(t, "", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("a registry being down must not stop add: %v\n%s", err, out)
	}
	wantContains(t, out, "Warning: registry test (test/presets) cannot be read: ", "Added myapp on local")
	if strings.Count(out, "Warning:") != 1 {
		t.Errorf("expected one line about it:\n%s", out)
	}
}

func TestAddSaysWhenARegistryNeedsANewerNearhost(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack")
	f.files["/test/presets/HEAD/index.yaml"] = "schema: 2\nref: " + refOne + "\n"
	out, err := executeWithInput(t, "", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("a newer registry must not stop add: %v\n%s", err, out)
	}
	wantContains(t, out, "Warning: registry test (test/presets) needs a newer nearhost than this one; upgrade nearhost to use it.\n", "Added myapp on local")
	if strings.Contains(out, "cannot be read") {
		t.Errorf("the registry was reached:\n%s", out)
	}
}

// One registry that cannot be read costs only its own suggestions.
func TestAddCarriesOnWhenOneRegistryIsDown(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack")
	trust(t, cfgPath, acmeReg)
	f.down[acmeReg.Repo] = true
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, askStack, "Warning: registry acme (Acme/web-presets) cannot be read: ", "  8080  web       App (APP_PORT)\n", "Added myapp on local")
	if strings.Count(out, "Warning:") != 1 {
		t.Errorf("expected one warning, for acme:\n%s", out)
	}
}

// Registries are read at the same time. The first registry answers only once
// the second has been asked, which a one-at-a-time read would never reach
// before the first timed out.
func TestAddReadsEveryRegistryAtOnce(t *testing.T) {
	cfgPath, f, b := suggesting(t, false, "stack")
	f.publish(acmeReg.Repo, refTwo, "stack")
	trust(t, cfgPath, acmeReg)
	f.hold(testReg.Repo, acmeReg.Repo)
	out, err := executeWithInput(t, "", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "`--preset test/stack`", "`--preset acme/stack`")
	if strings.Contains(out, "Warning:") {
		t.Errorf("both registries should have been read:\n%s", out)
	}
}

// Two registries that offer the same name are both offered, and taking one
// does not hide the other.
func TestAddOffersTheSameNameFromEachRegistry(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack")
	f.publish(acmeReg.Repo, refTwo, "stack")
	trust(t, cfgPath, acmeReg)
	out, err := executeWithInput(t, "y\ny\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	askAcme := "This looks like a Test stack project. Enable acme/stack from Acme/web-presets? [Y/n] "
	wantContains(t, out, askStack, askAcme, "Added myapp on local")
	if strings.Count(out, askStack) != 1 || strings.Count(out, askAcme) != 1 {
		t.Errorf("each should be offered once:\n%s", out)
	}
	cfg := loadSaved(t, cfgPath)
	for _, name := range []string{"test/stack", "acme/stack"} {
		if _, ok := cfg.Preset(name); !ok {
			t.Errorf("%s was not enabled: %+v", name, cfg.Presets)
		}
	}
}

// A preset that cannot be installed is reported, and the project is added
// with what discovery knows without it.
func TestAddCarriesOnWhenAnAcceptedPresetCannotBeInstalled(t *testing.T) {
	cfgPath, f, b := suggesting(t, true, "stack")
	f.files["/test/presets/"+refOne+"/presets/stack.yaml"] += "\n# tampered\n"
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "Warning: the test/stack preset was not enabled:", "does not match the registry index", "  8080  web     laravel.test (APP_PORT)\n", "Added myapp on local")
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

// Offer titles come from a registry's index, which is untrusted.
func TestAddMakesSuggestedTitlesSafeToPrint(t *testing.T) {
	for name, terminal := range map[string]bool{"with a terminal": true, "without a terminal": false} {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := suggesting(t, terminal, "stack")
			// The index refuses control characters, so what is left to strip
			// is other unprintable text, such as a right-to-left override.
			f.files["/test/presets/HEAD/index.yaml"] = strings.Replace(f.files["/test/presets/HEAD/index.yaml"], "title: Test stack", `title: "Evil‮ der​dro"`, 1)
			out, err := executeWithInput(t, "n\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			if strings.ContainsAny(out, "‮​") {
				t.Errorf("output carries unprintable text: %q", out)
			}
			wantContains(t, out, "This looks like a Evil derdro project. ")
		})
	}
}

// A title with nothing printable in it leaves the preset's name to say
// what the project looks like.
func TestAddNamesThePresetWhenItsTitleIsEmpty(t *testing.T) {
	for name, terminal := range map[string]bool{"with a terminal": true, "without a terminal": false} {
		t.Run(name, func(t *testing.T) {
			cfgPath, f, b := suggesting(t, terminal, "stack")
			f.files["/test/presets/HEAD/index.yaml"] = strings.Replace(f.files["/test/presets/HEAD/index.yaml"], "title: Test stack", `title: ""`, 1)
			out, err := executeWithInput(t, "n\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
			if err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}
			wantContains(t, out, "This looks like a test/stack project. ")
		})
	}
}
