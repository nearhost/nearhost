package service

import (
	"path/filepath"
	"strings"

	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/update"
)

// copyError is the service.copy error of a copy of nearhost that was made
// how, which will not be there at the next login.
func copyError(how string) error {
	return report.Errorf(report.ServiceCopy, "A service cannot run this copy of nearhost.").
		Because("This copy was %s, so it will not be there at the next login.", how).
		Step("Install nearhost as https://nearhost.dev/docs/getting-started/install/ says, then from that copy run:", "nearhost service install")
}

// ServiceExe returns the path the service should run for the running
// binary exe, and a notice for the user (nil for none). It refuses a copy
// that will not be there at the next login with a service.copy message.
func ServiceExe(goos, exe string, getenv func(string) string, lookPath func(string) (string, error)) (path string, note *report.Message, err error) {
	slashed := strings.ReplaceAll(exe, `\`, "/")
	kind := update.DetectOn(goos, exe, getenv).Kind
	if kind == update.Ephemeral {
		return "", nil, copyError("fetched by npx, bunx or pnpm dlx for one run")
	}
	if inGoBuild(slashed) {
		return "", nil, copyError("built by go run")
	}
	if kind == update.Homebrew {
		if p, e := lookPath("nearhost"); e == nil && sameFile(p, exe) {
			return p, nil, nil
		}
		return "", nil, report.Errorf(report.ServiceCopy, "A service cannot run this copy of nearhost.").
			Because("This copy is inside Homebrew's versioned folder, which the next upgrade removes.").
			Step("Run it as plain nearhost from Homebrew's bin folder:", "nearhost service install")
	}
	if after, ok := cutPath(goos, slashed, nodeVersions(getenv)); ok {
		v, _, _ := strings.Cut(after, "/")
		return exe, report.Notef(report.ServiceNodeVersion, "The service runs nearhost from Node %s's folder.", v).
			Step("If you change your default Node version, install the service again:", "nearhost service install"), nil
	}
	return exe, nil, nil
}

// nodeVersions is the slash-separated part of a path that nvm keeps Node's
// versions under: $NVM_DIR/versions/node/, or ~/.nvm's.
func nodeVersions(getenv func(string) string) string {
	if d := strings.TrimRight(strings.ReplaceAll(getenv("NVM_DIR"), `\`, "/"), "/"); d != "" {
		return d + "/versions/node/"
	}
	return "/.nvm/versions/node/"
}

// cutPath returns what follows the first sep in p, ignoring case on Windows,
// whose paths do, as DetectOn's do.
func cutPath(goos, p, sep string) (string, bool) {
	if goos != "windows" {
		_, after, ok := strings.Cut(p, sep)
		return after, ok
	}
	for i := 0; i+len(sep) <= len(p); i++ {
		if strings.EqualFold(p[i:i+len(sep)], sep) {
			return p[i+len(sep):], true
		}
	}
	return "", false
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

// inGoBuild reports whether the slash-separated path p is inside go run's
// build directory: a segment that is "go-build" or "go-build" and digits
// (the temporary directory is go-build123456), not one that merely starts
// with it, such as go-builder.
func inGoBuild(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if rest, ok := strings.CutPrefix(seg, "go-build"); ok && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}
