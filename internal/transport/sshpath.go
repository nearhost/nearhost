package transport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// foundSSH is the ssh found on PATH, kept once found so that every later
// command runs the same binary, whatever PATH or the directories on it become
// while Nearhost runs. Only a success is kept: a failed lookup is tried again
// at the next command, so an ssh installed while Nearhost runs is found.
var foundSSH struct {
	mu   sync.Mutex
	path string
}

// findSSH returns the absolute path of the ssh on PATH, looking it up on the
// first call that succeeds. Its error is exec.LookPath's, so it reads as the
// error of running "ssh" by name: not found, or found through a relative PATH
// entry (exec.ErrDot), which is refused. A symlink is kept as found, not
// followed to its target.
func findSSH() (string, error) {
	foundSSH.mu.Lock()
	defer foundSSH.mu.Unlock()
	if foundSSH.path != "" {
		return foundSSH.path, nil
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return "", err
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", err
	}
	foundSSH.path = path
	return path, nil
}

// SSHCommand returns the command that runs ssh with args, with env added to
// this process's environment. sshPath is the ssh binary, run as given; ""
// means the ssh found on PATH at the first command, run by its absolute path
// from then on. When there is none, the command fails to start with the error
// running "ssh" by name would give.
func SSHCommand(ctx context.Context, sshPath string, env []string, args ...string) *exec.Cmd {
	var err error
	if sshPath == "" {
		sshPath, err = findSSH()
	}
	if err != nil {
		// Name the command as before; Err keeps it from looking again.
		sshPath = "ssh"
	}
	cmd := exec.CommandContext(ctx, sshPath, args...)
	if err != nil {
		cmd.Err = err
	}
	cmd.Env = append(os.Environ(), env...)
	return cmd
}
