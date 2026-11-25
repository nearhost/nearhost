package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// upNoBackend is an up that fails the test if it reaches anything past the
// config check.
func upNoBackend(t *testing.T) upBackend {
	t.Helper()
	return upBackend{
		ensureHosts: func(context.Context, []string) error { t.Error("the hosts function was reached"); return nil },
	}
}

func TestLsNamesAMissingConfigFileGivenWithConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.yaml")
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newLsCmd(), "--config", path, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No projects in " + abs + " (the file does not exist yet).\n"; out != want {
		t.Fatalf("ls printed %q, want %q", out, want)
	}
}

func TestLsShowsWhereARelativeConfigWasLookedFor(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newLsCmd(), "--config", "nothere.yaml", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No projects in " + filepath.Join(wd, "nothere.yaml") + " (the file does not exist yet).\n"; out != want {
		t.Fatalf("ls printed %q, want %q", out, want)
	}
}

func TestLsKeepsTheNoProjectsTextForAMissingDefaultConfig(t *testing.T) {
	configHome(t)
	out, err := execute(t, newLsCmd(), "ls")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No projects yet. Add one with `nearhost add <path>`.\n"; out != want {
		t.Fatalf("ls printed %q, want %q", out, want)
	}
}

func TestLsKeepsTheNoProjectsTextForAnExistingEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("projects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newLsCmd(), "--config", path, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No projects yet. Add one with `nearhost add <path>`.\n"; out != want {
		t.Fatalf("ls printed %q, want %q", out, want)
	}
}

func TestLsKeepsItsOwnErrorForAConfigThatIsADirectory(t *testing.T) {
	out, err := execute(t, newLsCmd(), "--config", t.TempDir(), "ls")
	if err == nil || strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("ls on a directory: err %v, out %q", err, out)
	}
}

func TestUpNamesAMissingConfigFileGivenWithConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.yaml")
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newUpCmd(upNoBackend(t)), "--config", path, "up")
	if err == nil {
		t.Fatalf("up succeeded:\n%s", out)
	}
	if want := "No projects in " + abs + " (the file does not exist yet)."; err.Error() != want {
		t.Fatalf("up failed with %q, want %q", err, want)
	}
}

func TestUpShowsWhereARelativeConfigWasLookedFor(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, newUpCmd(upNoBackend(t)), "--config", "nothere.yaml", "up")
	if want := "No projects in " + filepath.Join(wd, "nothere.yaml") + " (the file does not exist yet)."; err == nil || err.Error() != want {
		t.Fatalf("up failed with %v, want %q", err, want)
	}
}

func TestUpKeepsTheNoProjectsErrorForAMissingDefaultConfig(t *testing.T) {
	configHome(t)
	_, err := execute(t, newUpCmd(upNoBackend(t)), "up")
	wantNoProjects(t, err)
}

// wantNoProjects fails the test unless err is core.no-projects, which says
// how to add one.
func wantNoProjects(t *testing.T, err error) {
	t.Helper()
	m := messageIn(t, err)
	if m.Code != report.CoreNoProjects || m.Title != "There are no projects to start." {
		t.Fatalf("up failed with %v (%q)", err, m.Code)
	}
	wantSteps(t, m, report.Step{Text: "Add the project in a directory:", Command: "nearhost add <path>"})
}

func TestUpKeepsTheNoProjectsErrorForAnExistingEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("projects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, newUpCmd(upNoBackend(t)), "--config", path, "up")
	wantNoProjects(t, err)
}
