package service

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/nearhost/nearhost/internal/update"
)

const nvmDir = "/.nvm/versions/node/"

func copyError(how string) error {
	return errors.New("This copy of nearhost was " + how + " for one run, so a service could not start it later. " +
		"Install nearhost (https://nearhost.dev/docs/install/), then run nearhost service install from that copy.")
}

// ServiceExe returns the path the service should run for the running
// binary exe, and a note for the user ("" for none). It refuses a copy that
// will not be there at the next login.
func ServiceExe(goos, exe string, getenv func(string) string, lookPath func(string) (string, error)) (path, note string, err error) {
	slashed := filepath.ToSlash(exe)
	if goos == "windows" {
		slashed = strings.ReplaceAll(exe, `\`, "/")
	}
	if update.DetectOn(goos, exe, getenv).Kind == update.Ephemeral {
		return "", "", copyError("fetched by npx")
	}
	if strings.Contains(slashed, "/go-build") {
		return "", "", copyError("built by go run")
	}
	if update.DetectOn(goos, exe, getenv).Kind == update.Homebrew {
		if p, e := lookPath("nearhost"); e == nil && sameFile(p, exe) {
			return p, "", nil
		}
		return "", "", errors.New("This copy is inside Homebrew's versioned folder, which the next upgrade removes. " +
			"Run nearhost service install as plain nearhost from Homebrew's bin folder.")
	}
	if _, after, ok := strings.Cut(slashed, nvmDir); ok {
		v, _, _ := strings.Cut(after, "/")
		return exe, "The service runs nearhost from Node " + v + "'s folder. " +
			"If you change your default Node version, run nearhost service install again.", nil
	}
	return exe, "", nil
}

// sameFile reports whether a and b resolve to the same file.
func sameFile(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}
