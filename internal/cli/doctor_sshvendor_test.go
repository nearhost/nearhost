package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	testSystemRoot   = `C:\Windows`
	testProgramFiles = `C:\Program Files`
	vendorAdvice     = "Nearhost is tested with Microsoft's; put C:\\Windows\\System32\\OpenSSH first on PATH."
)

func TestSSHVendorWarnsForGitsSSH(t *testing.T) {
	const path = `C:\Program Files\Git\usr\bin\ssh.exe`
	c := checkSSHVendor(path, testSystemRoot, testProgramFiles)
	if c.name != "ssh vendor" || c.level != levelWarn {
		t.Fatalf("check = %+v", c)
	}
	if c.detail != "ssh is "+path+", not Microsoft's OpenSSH." || c.fix != vendorAdvice {
		t.Fatalf("detail = %q, fix = %q", c.detail, c.fix)
	}
}

func TestSSHVendorAcceptsMicrosoftsLocations(t *testing.T) {
	for _, path := range []string{
		`C:\Windows\System32\OpenSSH\ssh.exe`,
		`c:\windows\system32\openssh\SSH.EXE`,
		`C:/Windows/System32/OpenSSH/ssh.exe`,
		`C:\Windows\System32\\OpenSSH\ssh.exe`,
		`C:\Windows\.\System32\OpenSSH\ssh.exe`,
		`C:\Program Files\OpenSSH\ssh.exe`,
		`C:\PROGRAM FILES\openssh\ssh.exe`,
	} {
		if c := checkSSHVendor(path, testSystemRoot, testProgramFiles); c.level != levelOK || c.detail != path || c.fix != "" {
			t.Errorf("%s: check = %+v", path, c)
		}
	}
}

// A directory whose name only starts with OpenSSH is not Microsoft's.
func TestSSHVendorRejectsLookalikeDirectories(t *testing.T) {
	for _, path := range []string{
		`C:\Windows\System32\OpenSSHx\ssh.exe`,
		`C:\Program Files\OpenSSH-Win64\ssh.exe`,
		`C:\Windows\System32\OpenSSH.exe`,
		`C:\Windows\System32\ssh.exe`,
		`D:\Windows\System32\OpenSSH\ssh.exe`,
	} {
		if c := checkSSHVendor(path, testSystemRoot, testProgramFiles); c.level != levelWarn {
			t.Errorf("%s: check = %+v", path, c)
		}
	}
}

// The advice names the real system directory, and falls back to C:\Windows
// when the environment gives none.
func TestSSHVendorAdviceUsesTheSystemRoot(t *testing.T) {
	c := checkSSHVendor(`D:\Git\usr\bin\ssh.exe`, `D:\WINNT`, testProgramFiles)
	if want := `put D:\WINNT\System32\OpenSSH first on PATH.`; !strings.HasSuffix(c.fix, want) {
		t.Errorf("fix = %q", c.fix)
	}
	if c := checkSSHVendor(`D:\WINNT\System32\OpenSSH\ssh.exe`, `D:\WINNT`, ""); c.level != levelOK {
		t.Errorf("custom root: %+v", c)
	}
	if c := checkSSHVendor(`D:\Git\ssh.exe`, "", ""); !strings.HasSuffix(c.fix, `C:\Windows\System32\OpenSSH first on PATH.`) {
		t.Errorf("empty root: %+v", c)
	}
}

func sshVendorBackend(path string, err error) doctorBackend {
	f := healthy()
	f.goos = "windows"
	b := f.backend()
	b.sshVersion = func(context.Context) (string, error) { return "OpenSSH_for_Windows_9.5p2, LibreSSL 3.8.2", nil }
	b.findSSH = func() (string, error) { return path, err }
	b.getenv = func(k string) string {
		return map[string]string{"SystemRoot": testSystemRoot, "ProgramFiles": testProgramFiles}[k]
	}
	return b
}

func TestDoctorReportsTheSSHVendorOnWindows(t *testing.T) {
	doctorConfig(t)
	b := sshVendorBackend(`C:\Program Files\Git\usr\bin\ssh.exe`, nil)
	out, err := execute(t, newDoctorCmd(b), "doctor", "--no-hosts")
	if err != nil {
		t.Fatalf("a warning must not fail doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ssh is C:\\Program Files\\Git\\usr\\bin\\ssh.exe, not Microsoft's OpenSSH.") ||
		!strings.Contains(out, vendorAdvice) {
		t.Errorf("missing warning in\n%s", out)
	}
}

func TestDoctorSaysNothingMoreWhenSSHIsNotFound(t *testing.T) {
	doctorConfig(t)
	b := sshVendorBackend("", errors.New("not found"))
	out, _ := execute(t, newDoctorCmd(b), "doctor", "--no-hosts")
	if strings.Contains(out, "ssh vendor") {
		t.Errorf("unexpected vendor check in\n%s", out)
	}
}

func TestDoctorChecksTheSSHVendorOnlyOnWindows(t *testing.T) {
	doctorConfig(t)
	b := sshVendorBackend(`C:\Program Files\Git\usr\bin\ssh.exe`, nil)
	b.goos = "linux"
	out, _ := execute(t, newDoctorCmd(b), "doctor", "--no-hosts")
	if strings.Contains(out, "ssh vendor") {
		t.Errorf("unexpected vendor check in\n%s", out)
	}
}
