package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/registry"
)

const (
	refOne = "1111111aaaaaaa"
	refTwo = "2222222bbbbbbb"
)

func TestPresetEnableInstallsFromTheRegistry(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack", "bundler")

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	if out != "Enabled preset stack (Test stack).\n" {
		t.Errorf("printed %q", out)
	}
	data, err := os.ReadFile(presetPath(cfgPath, "stack"))
	if err != nil {
		t.Fatalf("the preset was not installed: %v", err)
	}
	ref, ok := loadSaved(t, cfgPath).Preset("stack")
	if !ok || ref.Ref != refOne || ref.SHA256 != registry.Sum(data) {
		t.Errorf("config records %+v %v", ref, ok)
	}
	if _, err := os.Stat(presetPath(cfgPath, "bundler")); err == nil {
		t.Error("only the preset asked for should be installed")
	}
	if f.hits["/"+refOne+"/presets/stack.yaml"] != 1 {
		t.Errorf("hits = %v", f.hits)
	}
}

func TestPresetEnablePrefersALocalFile(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack")
	if err := os.MkdirAll(presetsDir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "schema: 1\nname: stack\ntitle: My own stack\ndetect: 'true'\n"
	if err := os.WriteFile(presetPath(cfgPath, "stack"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack")
	if err != nil || out != "Enabled preset stack (My own stack).\n" {
		t.Fatalf("enable: %q %v", out, err)
	}
	if ref, _ := loadSaved(t, cfgPath).Preset("stack"); ref.Ref != "" {
		t.Errorf("a local file is not a registry preset: %+v", ref)
	}
	if f.hits["/"+refOne+"/presets/stack.yaml"] != 0 {
		t.Error("the registry's copy must not be downloaded over a local file")
	}
}

// Disabling keeps the file. Enabling it again finds the registry still
// describes that exact file, so it stays a registry preset.
func TestPresetReEnableKeepsTheRegistryLink(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, b := newFakeRegistry(t, refOne, "stack")
	for _, args := range [][]string{{"enable", "stack"}, {"disable", "stack"}, {"enable", "stack"}} {
		if out, err := execute(t, newPresetCmd(b), append([]string{"--config", cfgPath, "preset"}, args...)...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if ref, _ := loadSaved(t, cfgPath).Preset("stack"); ref.Ref != refOne {
		t.Errorf("stack = %+v, want it linked to the registry again", ref)
	}
}

func TestPresetEnableFailuresInstallNothing(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(f *fakeRegistry, cfgPath string)
		want   []string
	}{
		{"unknown", func(*fakeRegistry, string) {}, []string{`no preset named "nope"`, "the registry does not list it"}},
		{"registry down", func(f *fakeRegistry, _ string) { f.down = true }, []string{`no preset named "nope"`, "could not be reached"}},
		{"registry off", func(_ *fakeRegistry, cfgPath string) {
			off := false
			saveConfig(t, cfgPath, &config.Config{Registry: &off})
		}, []string{`no preset named "nope"`, "switched off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			f, b := newFakeRegistry(t, refOne, "stack")
			tc.break_(f, cfgPath)
			_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "nope")
			if err == nil {
				t.Fatal("enable succeeded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if tc.name == "registry off" && f.total() != 0 {
				t.Errorf("registry: false must stop every request, saw %v", f.hits)
			}
		})
	}
}

func TestPresetEnableRefusesATamperedDownload(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack")
	f.files["/"+refOne+"/presets/stack.yaml"] += "\n# changed after the index was built\n"
	_, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack")
	if err == nil || !strings.Contains(err.Error(), "does not match the registry index") {
		t.Fatalf("expected a hash error, got %v", err)
	}
	if _, statErr := os.Stat(presetPath(cfgPath, "stack")); statErr == nil {
		t.Error("a download that fails its hash check must not be written")
	}
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Error("the config must not be written")
	}
}

func TestPresetListShowsWhatTheRegistryOffers(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack", "bundler")

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	want := "No presets are enabled.\n" +
		"\n" +
		"Available from the registry:\n" +
		"  NAME     TITLE\n" +
		"  stack    Test stack\n" +
		"  bundler  Test bundler\n" +
		"\n" +
		"Enable one with `nearhost preset enable <name>`.\n"
	if out != want {
		t.Errorf("list printed:\n%s\nwant:\n%s", out, want)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack"); err != nil {
		t.Fatal(err)
	}
	out, _ = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	wantContains(t, out, "  stack  registry 1111111  Test stack\n", "Available from the registry:\n", "  bundler  Test bundler\n")
	if strings.Count(out, "stack") != 2 {
		t.Errorf("an enabled preset should not also be listed as available:\n%s", out)
	}

	f.down = true
	os.RemoveAll(filepath.Join(filepath.Dir(cfgPath), "registry"))
	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list must work offline: %v", err)
	}
	wantContains(t, out, "  stack  registry 1111111  Test stack\n", "The registry could not be reached, so the presets it offers are not listed.\n")
}

func TestPresetUpdate(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack", "bundler")
	for _, name := range []string{"stack", "bundler"} {
		if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", name); err != nil {
			t.Fatal(err)
		}
	}

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil || out != "stack: up to date\nbundler: up to date\n" {
		t.Fatalf("update with nothing new: %q %v", out, err)
	}

	// The registry moves on: stack changes, bundler is withdrawn.
	f.extra["stack"] = "\n# a newer version\n"
	f.publish(refTwo, "stack")
	newSum := registry.Sum([]byte(f.files["/"+refTwo+"/presets/stack.yaml"]))

	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if out != "stack: updated from 1111111 to 2222222\nbundler: kept; the registry no longer lists it\n" {
		t.Errorf("update printed %q", out)
	}
	ref, _ := loadSaved(t, cfgPath).Preset("stack")
	if ref.Ref != refTwo || ref.SHA256 != newSum {
		t.Errorf("config records %+v", ref)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "stack")); registry.Sum(data) != newSum {
		t.Error("the new version was not written")
	}
}

func TestPresetUpdateKeepsLocalEdits(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	_, b := newFakeRegistry(t, refOne, "stack")
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack"); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(presetPath(cfgPath, "stack"))
	edited := append(append([]byte{}, original...), "\n# my change\n"...)
	if err := os.WriteFile(presetPath(cfgPath, "stack"), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if out != "stack: kept; the file has local edits. Run `nearhost preset update stack --force` to overwrite them\n" {
		t.Errorf("update printed %q", out)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "stack")); string(data) != string(edited) {
		t.Fatal("the edit was lost")
	}

	out, err = execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "stack", "--force")
	if err != nil || out != "stack: overwritten with the registry's version at 1111111\n" {
		t.Fatalf("update --force: %q %v", out, err)
	}
	if data, _ := os.ReadFile(presetPath(cfgPath, "stack")); string(data) != string(original) {
		t.Error("--force should restore the registry's version")
	}
}

func TestPresetUpdateRefusals(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack")
	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update")
	if err != nil || out != "No presets from the registry are enabled.\n" {
		t.Fatalf("update with nothing enabled: %q %v", out, err)
	}
	if f.total() != 0 {
		t.Errorf("nothing to update should mean no request: %v", f.hits)
	}

	enablePresets(t, cfgPath, "bundler")
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "bundler"); err == nil || !strings.Contains(err.Error(), "is a local file") {
		t.Errorf("expected a local-file error, got %v", err)
	}
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update", "nope"); err == nil || !strings.Contains(err.Error(), "is not enabled") {
		t.Errorf("expected a not-enabled error, got %v", err)
	}

	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "enable", "stack"); err != nil {
		t.Fatal(err)
	}
	f.down = true
	if _, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "update"); err == nil {
		t.Error("update needs the registry and should fail when it is down, even with a cached index")
	}
}

func TestPresetListMakesRegistryTitlesSafeToPrint(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	f, b := newFakeRegistry(t, refOne, "stack")
	// The index refuses control characters, so what is left to strip is
	// other unprintable text, such as a right-to-left override.
	f.files["/main/index.yaml"] = strings.Replace(f.files["/main/index.yaml"], "title: Test stack", `title: "Evil\u202e der\u200bdro"`, 1)

	out, err := execute(t, newPresetCmd(b), "--config", cfgPath, "preset", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if strings.ContainsAny(out, "\u202e\u200b") {
		t.Errorf("output carries unprintable text: %q", out)
	}
	if !strings.Contains(out, "  stack  Evil derdro\n") {
		t.Errorf("the title should be printable:\n%q", out)
	}
}
