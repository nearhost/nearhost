package config

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// crossDeviceErr is what rename fails with between volumes.
var crossDeviceErr = windows.ERROR_NOT_SAME_DEVICE

func TestPathsOnWindows(t *testing.T) {
	appData, local := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("APPDATA", appData)
	t.Setenv("LOCALAPPDATA", local)
	dir := filepath.Join(appData, "nearhost")
	for _, tc := range []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"Dir", Dir, dir},
		{"DefaultPath", DefaultPath, filepath.Join(dir, "config.yaml")},
		{"RunDir", RunDir, filepath.Join(dir, "run")},
		{"DataDir", DataDir, filepath.Join(local, "nearhost")},
	} {
		if got, err := tc.got(); err != nil || got != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}
