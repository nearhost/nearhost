package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/registry"
)

const (
	refOne = "1111111aaaaaaa"
	refTwo = "2222222bbbbbbb"
)

// trusted is a config path whose config trusts the test registry, which
// offers stack and bundler at refOne.
func trusted(t *testing.T) (string, *fakeRegistries, presetBackend) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish(testReg.Repo, refOne, "stack", "bundler")
	trust(t, cfgPath, testReg)
	return cfgPath, f, b
}

func TestPresetEnableInstallsFromATrustedRegistry(t *testing.T) {
	cfgPath, f, b := trusted(t)
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	if out != "Enabled preset test/stack (Test stack).\n" {
		t.Errorf("printed %q", out)
	}
	file := filepath.Join(filepath.Dir(cfgPath), "presets", "test", "stack.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the preset was not installed in its registry's directory: %v", err)
	}
	ref, ok := loadSaved(t, cfgPath).Preset("test/stack")
	if !ok || ref.Ref != refOne || ref.SHA256 != registry.Sum(data) {
		t.Errorf("config records %+v %v", ref, ok)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), "registry", "test", "index.yaml")); err != nil {
		t.Errorf("the index was not cached in the registry's directory: %v", err)
	}
	if _, err := os.Stat(presetPath(cfgPath, "test/bundler")); err == nil {
		t.Error("only the preset asked for should be installed")
	}
	if f.hit("/test/presets/HEAD/index.yaml") != 1 || f.hit("/test/presets/"+refOne+"/presets/stack.yaml") != 1 || f.total() != 2 {
		t.Errorf("hits = %v", f.hits)
	}
}

func TestPresetEnableNeedsTheRegistryToBeTrusted(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish(acmeReg.Repo, refOne, "stack")
	_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "acme/stack")
	want := `no registry is trusted as "acme". Add one with: nearhost preset registry add <owner/repo> --as acme`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if f.total() != 0 {
		t.Errorf("an untrusted registry must not be asked: %v", f.hits)
	}
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Error("the config must not be written")
	}
}

// A plain name is a local file and nothing else, even when a trusted
// registry offers a preset of that name.
func TestPresetEnablePlainNameIsOnlyALocalFile(t *testing.T) {
	cfgPath, f, b := trusted(t)
	_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack")
	if err == nil || !strings.Contains(err.Error(), `no preset named "stack": there is no file `+presetPath(cfgPath, "stack")) || !strings.Contains(err.Error(), "<registry>/<name>") {
		t.Fatalf("expected a missing file error, got %v", err)
	}
	if f.total() != 0 {
		t.Errorf("a plain name must not reach a registry: %v", f.hits)
	}
}

// Disabling keeps the file. Enabling it again while the registry still
// offers that exact file uses it without a download.
func TestPresetReEnableUsesTheKeptFile(t *testing.T) {
	cfgPath, f, b := trusted(t)
	for _, args := range [][]string{{"enable", "test/stack"}, {"disable", "test/stack"}, {"enable", "test/stack"}} {
		if out, err := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath, "preset"}, args...)...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if ref, _ := loadSaved(t, cfgPath).Preset("test/stack"); ref.Ref != refOne {
		t.Errorf("test/stack = %+v, want it linked to the registry again", ref)
	}
	if n := f.hit("/test/presets/" + refOne + "/presets/stack.yaml"); n != 1 {
		t.Errorf("the preset was downloaded %d times, want once", n)
	}
}

// A kept file the registry has moved past is never swapped silently.
func TestPresetEnableRefusesAKeptFileThatDiffers(t *testing.T) {
	cfgPath, f, b := trusted(t)
	for _, args := range [][]string{{"enable", "test/stack"}, {"disable", "test/stack"}} {
		if out, err := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath, "preset"}, args...)...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	kept, _ := os.ReadFile(presetPath(cfgPath, "test/stack"))
	f.extra["stack"] = "\n# a newer version\n"
	f.publish(testReg.Repo, refTwo, "stack")
	os.RemoveAll(registryDir(cfgPath, "test")) // a day has passed

	_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack")
	if err == nil || !strings.Contains(err.Error(), "differs from the version registry test offers now") || !strings.Contains(err.Error(), "nearhost preset enable test/stack --force") {
		t.Fatalf("expected a refusal naming --force, got %v", err)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "test/stack")); string(data) != string(kept) {
		t.Error("the kept file was changed")
	}
	if _, ok := loadSaved(t, cfgPath).Preset("test/stack"); ok {
		t.Error("a refused enable must not enable the preset")
	}

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack", "--force")
	if err != nil || out != "Enabled preset test/stack (Test stack).\n" {
		t.Fatalf("enable --force: %q %v", out, err)
	}
	ref, _ := loadSaved(t, cfgPath).Preset("test/stack")
	data, _ := os.ReadFile(presetPath(cfgPath, "test/stack"))
	if ref.Ref != refTwo || registry.Sum(data) != ref.SHA256 || !strings.Contains(string(data), "a newer version") {
		t.Errorf("--force should install the registry's version: %+v", ref)
	}
}

func TestPresetEnableRefusesAKeptFileEditedByHand(t *testing.T) {
	cfgPath, _, b := trusted(t)
	for _, args := range [][]string{{"enable", "test/stack"}, {"disable", "test/stack"}} {
		if out, err := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath, "preset"}, args...)...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	file := presetPath(cfgPath, "test/stack")
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(file, append(data, "\n# mine\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a refusal naming --force, got %v", err)
	}
}

func TestPresetEnableFailuresInstallNothing(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		break_ func(f *fakeRegistries)
		want   []string
	}{
		{"unknown", "test/nope", func(*fakeRegistries) {}, []string{`no preset named "test/nope": registry test (test/presets) does not list nope`}},
		{"registry down", "test/stack", func(f *fakeRegistries) { f.down[testReg.Repo] = true }, []string{"registry test (test/presets) cannot be read: ", "404"}},
		{"tampered", "test/stack", func(f *fakeRegistries) { f.files["/test/presets/"+refOne+"/presets/stack.yaml"] += "\n# changed\n" }, []string{"does not match the registry index"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, f, b := trusted(t)
			tc.break_(f)
			_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", tc.preset)
			if err == nil {
				t.Fatal("enable succeeded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if _, statErr := os.Stat(presetsDir(cfgPath)); statErr == nil {
				t.Error("nothing may be written to the presets directory")
			}
			if cfg := loadSaved(t, cfgPath); len(cfg.Presets) != 0 {
				t.Errorf("presets = %+v", cfg.Presets)
			}
		})
	}
}

// Every segment of a preset's path is checked before it is used, so no name
// reaches outside its registry's directory.
func TestPresetNamesCannotLeaveTheirDirectory(t *testing.T) {
	cfgPath, f, b := trusted(t)
	for _, name := range []string{"test/../stack", "../test/stack", "test/a/b", "/stack", "test/", "Test/stack", "test/.."} {
		_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", name)
		if err == nil || !strings.Contains(err.Error(), "a preset name is") {
			t.Errorf("enable %q: expected a name error, got %v", name, err)
		}
	}
	if f.total() != 0 {
		t.Errorf("a bad name must not reach a registry: %v", f.hits)
	}
	if _, err := os.Stat(presetsDir(cfgPath)); err == nil {
		t.Error("nothing may be written")
	}
}

// Two registries may offer presets of the same name. Both are listed and
// installed side by side, each in its own directory.
func TestTwoRegistriesOfferingTheSameNameNeverClash(t *testing.T) {
	cfgPath, f, b := trusted(t)
	f.extra["stack"] = "\n# acme's version\n"
	f.publish(acmeReg.Repo, refTwo, "stack", "bundler")
	trust(t, cfgPath, acmeReg)

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	wantContains(t, out, "  test/stack    Test stack\n", "  acme/stack    Test stack\n")

	for _, name := range []string{"test/stack", "acme/stack"} {
		if out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", name); err != nil {
			t.Fatalf("enable %s: %v\n%s", name, err, out)
		}
	}
	mine, _ := os.ReadFile(presetPath(cfgPath, "test/stack"))
	theirs, _ := os.ReadFile(presetPath(cfgPath, "acme/stack"))
	if strings.Contains(string(mine), "acme's version") || !strings.Contains(string(theirs), "acme's version") {
		t.Error("each registry's stack should be kept in its own file")
	}
	cfg := loadSaved(t, cfgPath)
	if ref, _ := cfg.Preset("test/stack"); ref.Ref != refOne {
		t.Errorf("test/stack = %+v", ref)
	}
	if ref, _ := cfg.Preset("acme/stack"); ref.Ref != refTwo {
		t.Errorf("acme/stack = %+v", ref)
	}
	out, _ = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	wantContains(t, out, "  test/stack  test @ 1111111  Test stack\n", "  acme/stack  acme @ 2222222  Test stack\n")
}

func TestPresetListGroupsWhatEachRegistryOffers(t *testing.T) {
	cfgPath, f, b := trusted(t)
	f.publish(acmeReg.Repo, refTwo, "bundler")
	trust(t, cfgPath, acmeReg)

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	want := "No presets are enabled.\n" +
		"\n" +
		"Available from test (test/presets):\n" +
		"  NAME          TITLE\n" +
		"  test/stack    Test stack\n" +
		"  test/bundler  Test bundler\n" +
		"\n" +
		"Available from acme (Acme/web-presets):\n" +
		"  NAME          TITLE\n" +
		"  acme/bundler  Test bundler\n" +
		"\n" +
		"Enable one with `nearhost preset enable <registry>/<name>`.\n"
	if out != want {
		t.Errorf("list printed:\n%s\nwant:\n%s", out, want)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err != nil {
		t.Fatal(err)
	}
	out, _ = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	wantContains(t, out, "  test/stack  test @ 1111111  Test stack\n", "  test/bundler  Test bundler\n")
	if strings.Count(out, "test/stack") != 1 {
		t.Errorf("an enabled preset should not also be listed as available:\n%s", out)
	}
}

// One registry that cannot be read gets one line, and the others are listed.
func TestPresetListCarriesOnWhenOneRegistryIsDown(t *testing.T) {
	cfgPath, f, b := trusted(t)
	trust(t, cfgPath, acmeReg)
	f.down[acmeReg.Repo] = true
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list must work with a registry down: %v", err)
	}
	wantContains(t, out, "Available from test (test/presets):\n", "\nWarning: registry acme (Acme/web-presets) cannot be read: ")
	if strings.Count(out, "Warning:") != 1 {
		t.Errorf("expected one line about the registry that is down:\n%s", out)
	}
}

// A registry whose index is newer than this build was reached, so the line
// says to upgrade rather than that it could not be read.
func TestPresetListSaysWhenARegistryNeedsANewerNearhost(t *testing.T) {
	cfgPath, f, b := trusted(t)
	f.files["/test/presets/HEAD/index.yaml"] = "schema: 2\nref: " + refOne + "\n"
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	wantContains(t, out, "\nWarning: registry test (test/presets) needs a newer nearhost than this one; upgrade nearhost to use it.\n")
	if strings.Contains(out, "cannot be read") {
		t.Errorf("the registry was reached:\n%s", out)
	}
}

func TestPresetUpdate(t *testing.T) {
	cfgPath, f, b := trusted(t)
	for _, name := range []string{"test/stack", "test/bundler"} {
		if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", name); err != nil {
			t.Fatal(err)
		}
	}

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil || out != "test/stack: up to date\ntest/bundler: up to date\n" {
		t.Fatalf("update with nothing new: %q %v", out, err)
	}

	// The registry moves on: stack changes, bundler is withdrawn.
	f.extra["stack"] = "\n# a newer version\n"
	f.publish(testReg.Repo, refTwo, "stack")
	newSum := registry.Sum([]byte(f.files["/test/presets/"+refTwo+"/presets/stack.yaml"]))

	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if out != "test/stack: updated from 1111111 to 2222222\ntest/bundler: kept; the registry no longer lists it\n" {
		t.Errorf("update printed %q", out)
	}
	ref, _ := loadSaved(t, cfgPath).Preset("test/stack")
	if ref.Ref != refTwo || ref.SHA256 != newSum {
		t.Errorf("config records %+v", ref)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "test/stack")); registry.Sum(data) != newSum {
		t.Error("the new version was not written")
	}
}

func TestPresetUpdateKeepsLocalEdits(t *testing.T) {
	cfgPath, _, b := trusted(t)
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(presetPath(cfgPath, "test/stack"))
	edited := append(append([]byte{}, original...), "\n# my change\n"...)
	if err := os.WriteFile(presetPath(cfgPath, "test/stack"), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if out != "test/stack: kept; the file has local edits. Run `nearhost preset update test/stack --force` to overwrite them\n" {
		t.Errorf("update printed %q", out)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "test/stack")); string(data) != string(edited) {
		t.Fatal("the edit was lost")
	}

	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "test/stack", "--force")
	if err != nil || out != "test/stack: overwritten with the registry's version at 1111111\n" {
		t.Fatalf("update --force: %q %v", out, err)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "test/stack")); string(data) != string(original) {
		t.Error("--force should restore the registry's version")
	}
}

func TestPresetUpdateRefusals(t *testing.T) {
	cfgPath, f, b := trusted(t)
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil || out != "No presets from a registry are enabled.\n" {
		t.Fatalf("update with nothing enabled: %q %v", out, err)
	}
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "test")
	if err != nil || out != "No presets from registry test are enabled.\n" {
		t.Fatalf("update of a registry with nothing enabled: %q %v", out, err)
	}
	if f.total() != 0 {
		t.Errorf("nothing to update should mean no request: %v", f.hits)
	}

	enablePresets(t, cfgPath, "bundler")
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "bundler"); err == nil || !strings.Contains(err.Error(), "is a local file") {
		t.Errorf("expected a local-file error, got %v", err)
	}
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "nope"); err == nil || !strings.Contains(err.Error(), `no preset named "nope" is enabled and no registry is trusted as "nope"`) {
		t.Errorf("expected a not-enabled error, got %v", err)
	}
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "acme/stack"); err == nil || err.Error() != `preset "acme/stack" is not enabled` {
		t.Errorf("expected a plain not-enabled error for a qualified name, got %v", err)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err != nil {
		t.Fatal(err)
	}
	f.down[testReg.Repo] = true
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err == nil {
		t.Error("update needs the registry and should fail when it is down, even with a cached index")
	}
	wantContains(t, out, "test/stack: not updated: registry test (test/presets) cannot be read: ")
}

// Naming a registry updates only its presets and asks only it. A registry
// that is down stops only its own presets.
func TestPresetUpdateByRegistry(t *testing.T) {
	cfgPath, f, b := trusted(t)
	f.publish(acmeReg.Repo, refOne, "stack")
	trust(t, cfgPath, acmeReg)
	for _, name := range []string{"test/stack", "acme/stack"} {
		if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", name); err != nil {
			t.Fatal(err)
		}
	}
	before := f.requests(testReg.Repo)
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "acme")
	if err != nil || out != "acme/stack: up to date\n" {
		t.Fatalf("update acme: %q %v", out, err)
	}
	if f.requests(testReg.Repo) != before {
		t.Error("updating acme must not ask the test registry")
	}

	f.down[acmeReg.Repo] = true
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err == nil || err.Error() != "1 of 2 presets could not be updated" {
		t.Errorf("error = %v", err)
	}
	wantContains(t, out, "test/stack: up to date\n", "acme/stack: not updated: registry acme (Acme/web-presets) cannot be read: ")
}

func TestPresetListMakesRegistryTitlesSafeToPrint(t *testing.T) {
	cfgPath, f, b := trusted(t)
	// The index refuses control characters, so what is left to strip is
	// other unprintable text, such as a right-to-left override.
	f.files["/test/presets/HEAD/index.yaml"] = strings.Replace(f.files["/test/presets/HEAD/index.yaml"], "title: Test stack", "title: \"Evil\u202e der\u200bdro\"", 1)

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if strings.ContainsAny(out, "\u202e\u200b") {
		t.Errorf("output carries unprintable text: %q", out)
	}
	if !strings.Contains(out, "  test/stack    Evil derdro\n") {
		t.Errorf("the title should be printable:\n%q", out)
	}
}

// With no registry trusted, no command makes a request, and add makes none
// when the answer to trusting the official registry is no.
func TestNoRegistryMeansNoRequest(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistries(t)
	f.publish(testReg.Repo, refOne, "stack", "bundler")
	enablePresets(t, cfgPath, "stack")
	for _, args := range [][]string{{"preset", "list"}, {"preset", "update"}, {"preset", "enable", "bundler"}, {"preset", "enable", "test/bundler"}, {"preset", "disable", "stack"}, {"preset", "registry", "list"}, {"preset", "registry", "remove", "test"}} {
		execute(t, newPresetCmd(b), append([]string{"--config", cfgPath}, args...)...)
	}
	add := noRemote(t)
	add.presets = b
	add.interactive = func() bool { return true }
	if out, err := executeWithInput(t, "n\n", newAddCmd(add), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--web", "8080"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if f.total() != 0 {
		t.Errorf("no registry is trusted, so nothing may be requested: %v", f.hits)
	}
}

func TestInstallLeavesNoTempFile(t *testing.T) {
	cfgPath, _, b := trusted(t)
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "test/stack"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	dir := filepath.Join(filepath.Dir(cfgPath), "presets", "test")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "stack.yaml" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only stack.yaml", names)
	}
	info, err := os.Stat(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("preset mode = %v, want 0644", info.Mode().Perm())
	}
}
