package elevate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/winsys"
)

// runAs starts a program as administrator; tests replace it, and so does a
// build with the e2e tag, which no release is (see e2e_windows.go).
var runAs = winsys.RunAs

// promptTurn lets one UAC prompt be on screen at a time. ShellExecuteEx
// cannot be cancelled, so its holder gives the turn back only when runAs
// returns, which is when the prompt closes, not when its caller gives up.
var promptTurn = newTurn()

// started is what runAs gave back.
type started struct {
	h   windows.Handle
	err error
}

// System is this OS's Elevator.
func System() Elevator {
	return uac{}
}

// uac runs privileged steps through the UAC prompt. The elevated child gets
// no standard streams back to Nearhost, so its input and its report travel
// in files that Run names with --input and --report.
type uac struct{}

// Needed reports whether this process is not elevated.
func (uac) Needed() bool { return !Elevated() }

// TakesPassword is false: Windows asks for consent itself.
func (uac) TakesPassword() bool { return false }

// Cached is false: every Run asks for consent.
func (uac) Cached(context.Context) bool { return false }

// Forget does nothing: there are no cached credentials.
func (uac) Forget(context.Context) {}

// Validate is unsupported: there is no password to check.
func (uac) Validate(context.Context, string) error { return errors.ErrUnsupported }

// Run runs c through the UAC prompt and waits for it to finish. c.Input is
// written to a file named by --input, and when the child fails, the last
// line of the file named by --report is the error. Both files are in a new
// folder from HelperDir, which the elevated helper insists on, and the
// folder goes when Run returns, whether the child ran, failed to start or
// was declined. A child that refuses the folder writes no report and exits
// with HelperRefusedCode, which Run turns into an error that names the
// folder; other exits without a report give their code. When ctx ends, while the
// prompt is up or while the child runs, Run stops waiting and removes both
// files, without killing the child: one that has not read its input yet
// then fails, so an approval given after a stop changes nothing. c.Prompt
// and c.Interactive are ignored, as the prompt is Windows' own.
//
// Only one prompt shows at a time: Run first waits for promptTurn, and gives
// up with ctx's error if ctx ends while it waits, without launching. The
// turn stays held after Run gives up on a prompt, until the prompt closes,
// so a second Run cannot stack a prompt on the abandoned one. A user who
// still clicks Yes on the abandoned prompt starts a helper whose folder is
// gone, which it refuses and exits, so the late approval changes nothing.
func (uac) Run(ctx context.Context, c Command) error {
	tn := promptTurn
	if err := tn.acquire(ctx); err != nil {
		return err
	}
	launched := false
	defer func() {
		if !launched { // no goroutine took the turn
			tn.release()
		}
	}()
	// The turn may come as ctx ends; do not start a prompt nobody waits for.
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := HelperDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte(c.Input), 0o600); err != nil {
		return err
	}
	report := filepath.Join(dir, "report")
	args := append(slices.Clip(c.Args), "--input", input, "--report", report)
	// RunAs does not return while the prompt is up, so it runs on its own
	// goroutine and Run can stop waiting for it. The goroutine gives the
	// turn back as soon as runAs returns, which is when the prompt closes.
	// When Run has given up, it also closes the handle of a child that was
	// approved late, without waiting for that child, and reports nothing.
	// The folder is Run's alone, removed once when Run returns.
	result, abandoned := make(chan started), make(chan struct{})
	launched = true
	go func() {
		h, err := runAs(c.Path, args)
		tn.release()
		select {
		case result <- started{h, err}:
		case <-abandoned:
			if err == nil && h != 0 {
				windows.CloseHandle(h)
			}
		}
	}()
	var h windows.Handle
	select {
	case r := <-result:
		h, err = r.h, r.err
	case <-ctx.Done():
		close(abandoned)
		return ctx.Err()
	}
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return ErrDeclined
	}
	if err != nil {
		return fmt.Errorf("run %s as administrator: %w", c.Path, err)
	}
	defer windows.CloseHandle(h)
	for {
		ev, err := windows.WaitForSingleObject(h, 100)
		if err != nil {
			return fmt.Errorf("wait for %s: %w", c.Path, err)
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return fmt.Errorf("exit code of %s: %w", c.Path, err)
	}
	if code == 0 {
		return nil
	}
	if line := reportLine(report); line != "" {
		return errors.New(line)
	}
	if code == HelperRefusedCode {
		return fmt.Errorf("the administrator's helper refused the folder %s: it must be on a local NTFS or ReFS drive (check TMP)", dir)
	}
	return fmt.Errorf("exited with code %d", code)
}

// HelperDirPrefix starts the name of every folder HelperDir makes.
const HelperDirPrefix = "nearhost-elevate-"

// HelperDir makes a new folder for one elevated run's input and report, in
// the user's temp directory (TMP), with a random name that starts with
// HelperDirPrefix. Its owner is this process's user and its access list,
// protected from what the temp directory passes down, grants that user,
// SYSTEM and Administrators alone, so the elevated helper's check that no
// one else can write in it passes whatever the temp directory allows. The
// helper still refuses it when TMP is not on a local NTFS or ReFS drive: a
// network share, UNC or a Samba share on a drive letter, or a FAT32 or
// exFAT drive, which has no owner or access list.
func HelperDir() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("find this process's user: %w", err)
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return "", err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for range 10 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		dir := filepath.Join(os.TempDir(), HelperDirPrefix+hex.EncodeToString(b[:]))
		name, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return "", err
		}
		err = windows.CreateDirectory(name, sa)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return "", &os.PathError{Op: "mkdir", Path: dir, Err: err}
		}
	}
	return "", fmt.Errorf("make a folder in %s: every name tried was taken", os.TempDir())
}

// reportLine is the last non-blank line of the file at path, or "" when there
// is none or the file cannot be read.
func reportLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
