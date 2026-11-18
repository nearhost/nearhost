package update

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/fsretry"
)

// Windows cannot overwrite or delete a running .exe, but it can rename one,
// so an update moves the running copy out of the way first. It goes to
// %LOCALAPPDATA%\nearhost\old, out of the install folder, so npm can delete
// its package folder; when that rename fails (the folder is on another
// volume, or cannot be made) it goes beside itself as <exe>.old-<pid>.
// CleanOld deletes both on a later start, once nothing runs them.

// oldDir is %LOCALAPPDATA%\nearhost\old, or "" when LOCALAPPDATA is unset or
// not an absolute path.
func oldDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || !filepath.IsAbs(base) {
		return ""
	}
	return filepath.Join(base, "nearhost", "old")
}

// StepAside moves exe, the running binary, out of the way, and returns a
// function that moves it back over whatever is at exe by then.
func StepAside(exe string) (restore func() error, err error) {
	moved, err := moveAside(exe)
	if err != nil {
		return nil, err
	}
	return func() error {
		// npm may have removed the package folder; a failed installer may
		// have left a partial file at exe. The old copy is known to work,
		// so it goes back in either case.
		err := os.MkdirAll(filepath.Dir(exe), 0o755)
		if err == nil {
			err = os.Rename(moved, exe)
		}
		if err == nil {
			return nil
		}
		return keep(moved, exe, err)
	}, nil
}

// keep moves the old copy, which could not go back to exe, to a name
// CleanOld leaves alone, and says where it is.
func keep(moved, exe string, err error) error {
	kept := filepath.Join(filepath.Dir(moved), fmt.Sprintf("nearhost-kept-%d-%s.exe", os.Getpid(), randomHex()))
	if dir := oldDir(); dir != "" && strings.EqualFold(filepath.Dir(moved), dir) {
		kept = filepath.Join(filepath.Dir(dir), filepath.Base(kept)) // %LOCALAPPDATA%\nearhost
	}
	if os.Rename(moved, kept) == nil {
		return fmt.Errorf("the old nearhost is at %s; move it back to %s: %w", kept, exe, err)
	}
	return fmt.Errorf("the old nearhost is at %s; move it back to %s before running nearhost again, which would delete it: %w", moved, exe, err)
}

func moveAside(exe string) (string, error) {
	var errs []error
	if dir := oldDir(); dir != "" {
		ext := filepath.Ext(exe)
		stem := strings.TrimSuffix(filepath.Base(exe), ext)
		// The pid and a random part keep two updates, or a leftover, from
		// meeting.
		to := filepath.Join(dir, fmt.Sprintf("%s-%d-%s%s", stem, os.Getpid(), randomHex(), ext))
		err := os.MkdirAll(dir, 0o755)
		if err == nil {
			err = renameAside(exe, to)
		}
		if err == nil {
			return to, nil
		}
		errs = append(errs, err)
	}
	beside := exe + ".old-" + strconv.Itoa(os.Getpid())
	for _, to := range []string{beside, beside + "-" + randomHex()} {
		err := renameAside(exe, to)
		if err == nil {
			return to, nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("move %s out of the way: %w", exe, errors.Join(errs...))
}

// renameAside renames the running copy. A virus scanner or an indexer may
// hold the file for a moment, most of all one that was just written, so a
// rename refused for that reason is tried again for a short while.
func renameAside(from, to string) error {
	return fsretry.Rename(from, to, 50*time.Millisecond, 2*time.Second)
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CleanOld deletes what it can of the copies earlier updates moved aside:
// everything in %LOCALAPPDATA%\nearhost\old and <exe>.old-*. A copy still
// running cannot be deleted and is left for a later start.
func CleanOld(exe string) {
	if dir := oldDir(); dir != "" {
		if es, err := os.ReadDir(dir); err == nil {
			for _, e := range es {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	if exe == "" {
		return
	}
	dir, prefix := filepath.Dir(exe), strings.ToLower(filepath.Base(exe)+".old-")
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		if strings.HasPrefix(strings.ToLower(e.Name()), prefix) { // Windows names ignore case
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
