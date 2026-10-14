package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hint = "No preset registries are trusted. To use the official presets: nearhost preset registry add nearhost/presets\n"

func TestRegistryAddTrustsARepo(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish(acmeReg.Repo, refOne, "stack", "bundler")

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "add", "Acme/web-presets")
	if err != nil {
		t.Fatalf("registry add: %v\n%s", err, out)
	}
	want := "Trusted registry acme (Acme/web-presets). Its index at 1111111 offers 2 presets.\n" +
		"Enable one with `nearhost preset enable acme/<name>`; `nearhost preset list` shows them.\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if regs := loadSaved(t, cfgPath).Registries; len(regs) != 1 || regs[0] != acmeReg {
		t.Errorf("registries = %+v", regs)
	}
	if _, err := os.Stat(filepath.Join(registryDir(cfgPath, "acme"), "index.yaml")); err != nil {
		t.Errorf("the index should be cached: %v", err)
	}
	if f.total() != 1 {
		t.Errorf("only the index should be fetched: %v", f.hits)
	}
}

func TestRegistryAddWithAnAlias(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish("acme--labs/presets", refOne, "stack")
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "add", "acme--labs/presets", "--as", "labs")
	if err != nil {
		t.Fatalf("registry add: %v\n%s", err, out)
	}
	wantContains(t, out, "Trusted registry labs (acme--labs/presets). Its index at 1111111 offers 1 preset.\n")
	if _, ok := loadSaved(t, cfgPath).Trusted("labs"); !ok {
		t.Error("labs was not saved")
	}
}

// Every refusal saves nothing. A repo or alias that is not valid, or one
// that is already trusted, is refused before any request.
func TestRegistryAddRefusesWhatItCannotTrust(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		request bool
	}{
		{"no owner", []string{"presets"}, `"presets" is not a GitHub repository; write it as owner/name`, false},
		{"a path", []string{"acme/presets/main"}, "is not a GitHub repository", false},
		{"owner with a dot", []string{"ac.me/presets"}, "is not a GitHub repository", false},
		{"owner that makes no alias", []string{"acme--labs/presets"}, `the owner "acme--labs" does not make an alias`, false},
		{"bad alias", []string{"acme/presets", "--as", "Web"}, `alias "Web" must be lowercase`, false},
		{"alias taken", []string{"other/presets", "--as", "test"}, `a registry is already trusted as "test" (test/presets); pass another alias with --as`, false},
		{"repo taken", []string{"TEST/Presets", "--as", "again"}, `test/presets is already trusted as "test"`, false},
		{"unreachable", []string{"acme/gone"}, "registry acme (acme/gone) cannot be read: ", true},
		{"bad index", []string{"acme/bad"}, "is not a commit", true},
		{"newer index", []string{"acme/newer"}, "registry acme (acme/newer) needs a newer nearhost", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, f, b := trusted(t)
			f.files["/acme/bad/HEAD/index.yaml"] = "schema: 1\nref: not a commit\n"
			f.files["/acme/newer/HEAD/index.yaml"] = "schema: 2\nref: " + refOne + "\n"
			_, err := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath, "preset", "registry", "add"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error with %q, got %v", tc.want, err)
			}
			if tc.request && !strings.Contains(err.Error(), "nothing was saved") {
				t.Errorf("error %q should say nothing was saved", err)
			}
			if regs := loadSaved(t, cfgPath).Registries; len(regs) != 1 {
				t.Errorf("registries = %+v", regs)
			}
			if !tc.request && f.total() != 0 {
				t.Errorf("a refusal of the arguments must not make a request: %v", f.hits)
			}
			if _, err := os.Stat(registryDir(cfgPath, "acme")); err == nil {
				t.Error("nothing may be cached for a registry that was not added")
			}
		})
	}
}

func TestRegistryListSaysWhatIsCached(t *testing.T) {
	cfgPath, f, b := trusted(t)
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "list")
	if err != nil || out != "ALIAS  REPO          INDEX\ntest   test/presets  not fetched\n" {
		t.Fatalf("list before a fetch: %q %v", out, err)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list"); err != nil {
		t.Fatal(err)
	}
	trust(t, cfgPath, acmeReg)
	cache := filepath.Join(registryDir(cfgPath, "test"), "index.yaml")
	then := time.Now().Add(-3*time.Hour - time.Minute)
	if err := os.Chtimes(cache, then, then); err != nil {
		t.Fatal(err)
	}
	before := f.total()
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "list")
	want := "ALIAS  REPO              INDEX\n" +
		"test   test/presets      1111111, fetched 3 hours ago\n" +
		"acme   Acme/web-presets  not fetched\n"
	if err != nil || out != want {
		t.Errorf("list printed:\n%s\nwant:\n%s(%v)", out, want, err)
	}
	if f.total() != before {
		t.Error("registry list must not make a request")
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute:         "just now",
		30 * time.Second:     "just now",
		time.Minute:          "1 minute ago",
		59 * time.Minute:     "59 minutes ago",
		time.Hour:            "1 hour ago",
		47 * time.Hour:       "47 hours ago",
		72 * time.Hour:       "3 days ago",
		24 * time.Hour * 400: "400 days ago",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRegistryRemove(t *testing.T) {
	cfgPath, _, b := trusted(t)
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/bundler"); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "remove", "test")
	if err == nil || err.Error() != "presets from registry test are enabled: test/stack, test/bundler. Disable them first with `nearhost preset disable <name>`" {
		t.Fatalf("expected a refusal listing the presets, got %v", err)
	}
	if len(loadSaved(t, cfgPath).Registries) != 1 {
		t.Fatal("a refused remove must keep the registry")
	}

	for _, name := range []string{"test/stack", "test/bundler"} {
		if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "disable", name); err != nil {
			t.Fatal(err)
		}
	}
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "remove", "test")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if out != "Removed registry test (test/presets). The presets installed from it are kept in "+filepath.Join(presetsDir(cfgPath), "test")+".\n" {
		t.Errorf("printed %q", out)
	}
	if len(loadSaved(t, cfgPath).Registries) != 0 {
		t.Error("the registry is still trusted")
	}
	if _, err := os.Stat(registryDir(cfgPath, "test")); err == nil {
		t.Error("the cached index should be deleted")
	}
	if _, err := os.Stat(presetPath(cfgPath, "test/stack")); err != nil {
		t.Errorf("preset files are kept: %v", err)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "remove", "test"); err == nil || err.Error() != `no registry is trusted as "test"` {
		t.Errorf("second remove: %v", err)
	}
}

// The hint shows where registry presets would be, and only while no registry
// is trusted.
func TestTheHintShowsOnlyWithoutRegistries(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish(testReg.Repo, refOne, "stack")

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil || out != "No presets are enabled.\n\n"+hint {
		t.Errorf("preset list: %q %v", out, err)
	}
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "registry", "list")
	if err != nil || out != hint {
		t.Errorf("registry list: %q %v", out, err)
	}
	add := noRemote(t)
	add.presets = b
	out, err = execute(t, newAddCmd(add), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "one", "--web", "8080")
	if err != nil || !strings.HasPrefix(out, hint+"\n") {
		t.Errorf("add: %v\n%s", err, out)
	}
	if f.total() != 0 {
		t.Errorf("the hint is local text and needs no request: %v", f.hits)
	}

	trust(t, cfgPath, testReg)
	for _, args := range [][]string{{"preset", "list"}, {"preset", "registry", "list"}} {
		if out, _ := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath}, args...)...); strings.Contains(out, "No preset registries") {
			t.Errorf("%v shows the hint with a registry trusted:\n%s", args, out)
		}
	}
	if out, _ := execute(t, newAddCmd(add), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "two", "--web", "8080"); strings.Contains(out, "No preset registries") {
		t.Errorf("add shows the hint with a registry trusted:\n%s", out)
	}
}

// add prints the hint only when no preset applied.
func TestAddLeavesOutTheHintWhenAPresetApplied(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	enablePresets(t, cfgPath, "stack")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "No preset registries") {
		t.Errorf("a preset applied, so no hint:\n%s", out)
	}
	out, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "other", "--no-presets", "--web", "8080")
	if err != nil || strings.Contains(out, "No preset registries") {
		t.Errorf("--no-presets asks for no presets, so no hint: %v\n%s", err, out)
	}
}
