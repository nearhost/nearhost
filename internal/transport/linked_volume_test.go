package transport

import (
	"io/fs"
	"strings"
	"testing"
	"time"
)

// volInfo is a minimal fs.FileInfo for the fake file system below.
type volInfo struct{ mode fs.FileMode }

// Name is empty.
func (volInfo) Name() string { return "" }

// Size is zero.
func (volInfo) Size() int64 { return 0 }

// Mode is the entry's mode.
func (i volInfo) Mode() fs.FileMode { return i.mode }

// ModTime is zero.
func (volInfo) ModTime() time.Time { return time.Time{} }

// IsDir reports whether the entry is a folder.
func (i volInfo) IsDir() bool { return i.mode.IsDir() }

// Sys is nil.
func (volInfo) Sys() any { return nil }

func TestLinkedTargetWithAVolume(t *testing.T) {
	entries := map[string]fs.FileMode{
		"C:":                  fs.ModeDir,
		"C:/proj":             fs.ModeDir,
		"C:/proj/ok":          fs.ModeSymlink,
		"C:/proj/.nearhostrc": 0,
		"C:/proj/out":         fs.ModeSymlink,
		"D:":                  fs.ModeDir,
		"D:/elsewhere":        fs.ModeDir,
		"D:/elsewhere/rc":     0,
	}
	links := map[string]string{"C:/proj/ok": "sub/x", "C:/proj/out": "D:/elsewhere/rc"}
	lstat := func(p string) (fs.FileInfo, error) {
		if strings.HasPrefix(p, "/C:") || strings.HasPrefix(p, "/D:") {
			t.Errorf("lstat(%q) starts with a slash before the drive", p)
		}
		m, ok := entries[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return volInfo{m}, nil
	}
	readlink := func(p string) (string, error) { return links[p], nil }

	got, err := linkedTarget("C:/proj", ".nearhostrc", lstat, readlink)
	if err != nil || got != "C:/proj/.nearhostrc" {
		t.Errorf("linkedTarget = %q, %v; want C:/proj/.nearhostrc", got, err)
	}
	if got, err := linkedTarget("C:/proj", "ok", lstat, readlink); err != nil || got != "C:/proj/sub/x" {
		t.Errorf("linkedTarget(ok) = %q, %v; want C:/proj/sub/x", got, err)
	}
	if _, err := linkedTarget("C:/proj", "out", lstat, readlink); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("link to D: = %v; want outside the project", err)
	}
}

func TestLinkedTargetClampsAtTheVolumeRoot(t *testing.T) {
	entries := map[string]fs.FileMode{
		"C:": fs.ModeDir, "C:/proj": fs.ModeDir, "C:/proj/up": fs.ModeSymlink, "C:/proj/.nearhostrc": 0,
	}
	links := map[string]string{"C:/proj/up": "../../proj/.nearhostrc"}
	lstat := func(p string) (fs.FileInfo, error) {
		if !strings.HasPrefix(p, "C:") {
			t.Errorf("lstat(%q) left the drive", p)
		}
		m, ok := entries[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return volInfo{m}, nil
	}
	readlink := func(p string) (string, error) { return links[p], nil }
	got, err := evalLinks("C:/proj/up", lstat, readlink)
	if err != nil || got != "C:/proj/.nearhostrc" {
		t.Errorf("evalLinks = %q, %v; want C:/proj/.nearhostrc", got, err)
	}
}

func TestEvalLinksDriveShapedTargetOnUnix(t *testing.T) {
	entries := map[string]fs.FileMode{"/proj": fs.ModeDir, "/proj/l": fs.ModeSymlink, "/proj/a:": fs.ModeDir, "/proj/a:/x": 0}
	lstat := func(p string) (fs.FileInfo, error) {
		m, ok := entries[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return volInfo{m}, nil
	}
	for _, dest := range []string{"a:/x", "a:"} {
		want := "/proj/" + dest
		got, err := evalLinks("/proj/l", lstat, func(string) (string, error) { return dest, nil })
		if err != nil || got != want {
			t.Errorf("link to %q = %q, %v; want %q", dest, got, err, want)
		}
	}
}

// volFS is a fake file system of slash-separated paths for evalLinks: the
// entries' modes and the links' targets. It fails the test on an lstat of a
// path with a slash before its drive.
func volFS(t *testing.T, entries map[string]fs.FileMode, links map[string]string) (func(string) (fs.FileInfo, error), func(string) (string, error)) {
	lstat := func(p string) (fs.FileInfo, error) {
		if strings.HasPrefix(p, "/C:") || strings.HasPrefix(p, "/D:") {
			t.Errorf("lstat(%q) starts with a slash before the drive", p)
		}
		m, ok := entries[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return volInfo{m}, nil
	}
	readlink := func(p string) (string, error) { return links[p], nil }
	return lstat, readlink
}

func TestLinkedTargetFollowsTheDriveALinkSwitchesTo(t *testing.T) {
	lstat, readlink := volFS(t, map[string]fs.FileMode{
		"C:": fs.ModeDir, "C:/proj": fs.ModeDir, "C:/proj/.nearhostrc": fs.ModeSymlink,
		"D:": fs.ModeDir, "D:/out": fs.ModeDir, "D:/proj": fs.ModeDir,
	}, map[string]string{"C:/proj/.nearhostrc": "D:/out/../proj/.nearhostrc"})
	got, err := evalLinks("C:/proj/.nearhostrc", lstat, readlink)
	if err != nil || got != "D:/proj/.nearhostrc" {
		t.Errorf("evalLinks = %q, %v; want D:/proj/.nearhostrc", got, err)
	}
	if _, err := linkedTarget("C:/proj", ".nearhostrc", lstat, readlink); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("linkedTarget = %v; want outside the project", err)
	}
}

func TestLinkedTargetRootRelativeAfterADriveSwitch(t *testing.T) {
	lstat, readlink := volFS(t, map[string]fs.FileMode{
		"C:": fs.ModeDir, "C:/proj": fs.ModeDir, "C:/proj/a": fs.ModeSymlink,
		"D:": fs.ModeDir, "D:/b": fs.ModeSymlink,
	}, map[string]string{"C:/proj/a": "D:/b", "D:/b": "/proj/.nearhostrc"})
	got, err := evalLinks("C:/proj/a", lstat, readlink)
	if err != nil || got != "D:/proj/.nearhostrc" {
		t.Errorf("evalLinks = %q, %v; want D:/proj/.nearhostrc", got, err)
	}
}

func TestEvalLinksRefusesADriveRelativeTarget(t *testing.T) {
	for _, dest := range []string{"C:", "C:foo", "d:proj/x"} {
		lstat, readlink := volFS(t, map[string]fs.FileMode{
			"C:": fs.ModeDir, "C:/proj": fs.ModeDir, "C:/proj/l": fs.ModeSymlink,
		}, map[string]string{"C:/proj/l": dest})
		got, err := evalLinks("C:/proj/l", lstat, readlink)
		if err == nil || !strings.Contains(err.Error(), "drive-relative") {
			t.Errorf("link to %q = %q, %v; want a drive-relative error", dest, got, err)
		}
	}
}

func TestLinkedTargetIgnoresCaseOnAVolume(t *testing.T) {
	lstat, readlink := volFS(t, map[string]fs.FileMode{
		"C:": fs.ModeDir, "C:/proj": fs.ModeDir, "C:/proj/.nearhostrc": fs.ModeSymlink,
	}, map[string]string{"C:/proj/.nearhostrc": "../PROJ/config"})
	got, err := linkedTarget("C:/proj", ".nearhostrc", lstat, readlink)
	if err != nil || got != "C:/PROJ/config" {
		t.Errorf("linkedTarget = %q, %v; want C:/PROJ/config", got, err)
	}
}

func TestLinkedTargetKeepsCaseOnASlashWalk(t *testing.T) {
	lstat, readlink := volFS(t, map[string]fs.FileMode{
		"/proj": fs.ModeDir, "/proj/.nearhostrc": fs.ModeSymlink,
	}, map[string]string{"/proj/.nearhostrc": "../PROJ/config"})
	if _, err := linkedTarget("/proj", ".nearhostrc", lstat, readlink); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("linkedTarget = %v; want outside the project", err)
	}
}
