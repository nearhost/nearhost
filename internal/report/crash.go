package report

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
)

// Version is the Nearhost version written in crash files. The cli sets it
// at start.
var Version = "dev"

// CrashDir is the folder Go writes crash files to, beside the core log.
// The cli sets it at start; when it is "", the system's temporary folder
// is used.
var CrashDir = ""

// IssuesURL is where a crash is reported.
const IssuesURL = "https://github.com/nearhost/nearhost/issues"

// Go runs f in a new goroutine. If f panics, the panic is written to a
// crash file in CrashDir and a Crash message is reported to ctx's sink, so
// the user sees a clear message rather than a stack trace. The goroutine
// then ends; what to do next (quit, carry on) is the sink's owner's call.
func Go(ctx context.Context, f func()) {
	go func() {
		defer func() {
			if v := recover(); v != nil {
				To(ctx, Crashed(v, debug.Stack()))
			}
		}()
		f()
	}()
}

// Crashed writes a crash file for a panic with value v and stack, in
// CrashDir, and returns the Crash message that names it. Use it where a
// panic is caught other than by Go.
func Crashed(v any, stack []byte) *Message { return CrashedIn(CrashDir, v, stack) }

// CrashedIn is Crashed writing the crash file to dir, or to the system's
// temporary folder when dir is "": for a caller that knows the core log's
// folder without setting CrashDir, which other goroutines may read.
func CrashedIn(dir string, v any, stack []byte) *Message {
	if dir == "" {
		dir = os.TempDir()
	}
	m := Errorf(Crash, "Nearhost stopped unexpectedly.")
	path, err := CrashFile(dir, v, stack)
	if err != nil {
		return m.Because("Nearhost could not save the details (%v). The problem was: %v", err, v).
			Then("Please report this at " + IssuesURL + ", with what you were doing.")
	}
	return m.Because("The details are in %s.", path).
		Then("Please report this at "+IssuesURL+", and attach that file.").
		With("file", path)
}

// CrashFile writes a crash report for a panic with value v and its stack
// to dir, as crash-<UTC time>.log, and returns its path. The file holds
// Nearhost's version, the OS and architecture, the time, the value and the
// stack, and nothing from the config. dir is made if it is missing.
func CrashFile(dir string, v any, stack []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	body := fmt.Sprintf("Nearhost %s crashed.\n\nVersion: %s\nOS: %s/%s\nGo: %s\nTime: %s\nPanic: %v\n\n%s",
		Version, Version, runtime.GOOS, runtime.GOARCH, runtime.Version(), now.Format(time.RFC3339), v, stack)
	base := "crash-" + now.Format("20060102T150405Z")
	for i := 1; ; i++ {
		name := base + ".log"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.log", base, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) && i < 100 {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", werr
		}
		return path, nil
	}
}
