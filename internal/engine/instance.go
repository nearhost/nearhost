package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/report"
)

// instanceLockName is the file in the run directory that a started engine
// holds a lock on until Shutdown. Each server's ssh master socket lives in
// the run directory, and a starting engine ends any master it finds there,
// so two engines sharing one would end each other's connections.
//
// The file holds the pid of the engine's process, for the message a second
// engine gives. It is kept after use, like the config's lock file: removing
// it would let an engine that opened it before the removal and one that
// opens it after hold the lock at once.
const instanceLockName = "nearhost.lock"

// alreadyRunning is a start refused because another engine holds the run
// directory: core.already-running, naming the holder's pid when the
// instance file says it (pid > 0). It matches ErrAlreadyRunning.
//
// It names no way to run a second one: a second Nearhost with its own
// config folder would still share the data folder, and so the local CA and
// the certificates, which the proxy takes to be its own.
func alreadyRunning(pid int) *report.Message {
	running := "Another Nearhost is already running"
	if pid > 0 {
		running += fmt.Sprintf(" (pid %d)", pid)
	}
	m := report.Errorf(report.CoreAlreadyRunning, "%s.", running).
		Then("Stop it with `nearhost stop`, or see it with `nearhost status`.").
		Wrap(ErrAlreadyRunning)
	if pid > 0 {
		m.With("pid", pid)
	}
	return m
}

// ErrAlreadyRunning is what Start's refusal matches, with errors.Is, when
// another Nearhost holds the run directory.
var ErrAlreadyRunning = errors.New("another Nearhost is already running")

// IsAlreadyRunning reports whether err is Start refusing because another
// Nearhost holds the run directory. Its message already says what to do.
func IsAlreadyRunning(err error) bool { return errors.Is(err, ErrAlreadyRunning) }

// lockInstance takes the lock on the instance file in dir without waiting,
// writes this process's pid in it and returns what releases the lock. When
// another holder has it, in this process or another, it returns the
// alreadyRunning message naming that holder's pid. A file left by a process that has
// exited holds no lock and does not stand in the way.
func lockInstance(dir string) (release func(), err error) {
	path := filepath.Join(dir, instanceLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	held, err := tryLockFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if held {
		pid := readPid(path)
		f.Close()
		return nil, alreadyRunning(pid)
	}
	release = func() {
		unlockFile(f)
		f.Close()
	}
	if err := writePid(f); err != nil {
		release()
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return release, nil
}

// InstanceRunning reports whether an engine, in this process or another,
// holds the instance lock in dir, the run directory: whether a Nearhost
// runs. It changes nothing on disk. It takes the lock for an instant when
// it is free, so an engine that starts at that instant is refused as if
// another ran; callers are commands a user runs by hand.
func InstanceRunning(dir string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, instanceLockName), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // no engine has started with this run directory
	}
	if err != nil {
		return false, fmt.Errorf("open instance lock: %w", err)
	}
	defer f.Close()
	held, err := tryLockFile(f)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	if !held {
		unlockFile(f)
	}
	return held, nil
}

// writePid replaces what f holds with this process's pid.
func writePid(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// readPid is the pid the instance file at path holds, or 0 when it holds
// none: the holder may not have written it yet.
func readPid(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
