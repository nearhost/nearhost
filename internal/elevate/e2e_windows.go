//go:build e2e

package elevate

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// This file is in builds made with the e2e tag alone, which the journeys in
// internal/e2e make and no release does: TestAReleaseBuildHasNoTestSeam
// there checks a build without the tag. In such a build nothing asks
// Windows for consent, so a journey can approve or decline the prompt
// without a person at the machine. The rest of the UAC elevator is the
// release's own: its turns, the helper's folder, the input and the report.

func init() { runAs = consent }

// Elevated is false in this build, whatever the process's token says. A
// run that believes it is elevated asks for nothing and writes the
// machine's own hosts file alone, and GitHub's Windows runners are
// elevated.
func Elevated() bool { return false }

// consentWait is how long the stand-in's prompt waits for an answer, as
// Windows' own does before it reports the prompt cancelled.
const consentWait = 2 * time.Minute

// consent stands in for the UAC prompt. It appends the command it is asked
// to run to the file $NEARHOST_E2E_CONSENT names with ".asked" after it,
// then waits for the file $NEARHOST_E2E_CONSENT names, which is the answer:
// it removes the file, and "yes" in it starts the command, as this user and
// not as administrator. Any other answer, none in consentWait, or no file
// named, is a declined prompt.
func consent(file string, args []string) (windows.Handle, error) {
	answer := os.Getenv("NEARHOST_E2E_CONSENT")
	if answer == "" {
		return 0, windows.ERROR_CANCELLED
	}
	asked, err := os.OpenFile(answer+".asked", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	_, werr := fmt.Fprintln(asked, strings.Join(append([]string{file}, args...), " "))
	if cerr := asked.Close(); werr != nil || cerr != nil {
		return 0, fmt.Errorf("write %s: %v, %v", asked.Name(), werr, cerr)
	}
	said, deadline := "", time.Now().Add(consentWait)
	for {
		// A file still being put in place cannot be read or removed yet.
		if b, err := os.ReadFile(answer); err == nil && os.Remove(answer) == nil {
			said = strings.TrimSpace(string(b))
			break
		}
		if time.Now().After(deadline) {
			return 0, windows.ERROR_CANCELLED
		}
		time.Sleep(20 * time.Millisecond)
	}
	if said != "yes" {
		return 0, windows.ERROR_CANCELLED
	}
	cmd := exec.Command(file, args...)
	// As the elevated helper, it has no console of nearhost's to write on.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Opened while cmd still holds the process, so the id is its own.
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	_ = cmd.Process.Release()
	return h, err
}
