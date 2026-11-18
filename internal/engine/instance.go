package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// instanceError is a start refused because another engine holds the run
// directory.
type instanceError struct {
	pid int // the holder's pid, 0 when the file does not say
}

// Error says that another Nearhost runs, and what to do about it.
func (e *instanceError) Error() string {
	running := "Another Nearhost is running with this config"
	if e.pid > 0 {
		running += fmt.Sprintf(" (pid %d)", e.pid)
	}
	return running + ". Stop it first, or pass --config for a separate one."
}

// lockInstance takes the lock on the instance file in dir without waiting,
// writes this process's pid in it and returns what releases the lock. When
// another holder has it, in this process or another, it returns an
// *instanceError naming that holder's pid. A file left by a process that has
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
		return nil, &instanceError{pid: pid}
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
