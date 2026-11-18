//go:build windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hosts"
)

// helperDir is a folder the UAC elevator would make, removed when the test
// ends.
func helperDir(t *testing.T) string {
	t.Helper()
	dir, err := elevate.HelperDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// writeInput writes the host list a.test to dir's file name and returns its
// path.
func writeInput(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("a.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// mkdirWithSDDL makes the folder name in a new temp directory with the
// security descriptor sddl and returns its path.
func mkdirWithSDDL(t *testing.T, name, sddl string) string {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), name)
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, sa); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mySID is the string SID of this process's user.
func mySID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// An elevated apply takes the input and report of a folder the elevator
// made. The test's hosts file is not the system's, so apply then fails, and
// the report says so in Nearhost's own words.
func TestElevatedHostsApplyTakesTheElevatorsFolderOnWindows(t *testing.T) {
	runElevated(t)
	dir := helperDir(t)
	in := writeInput(t, dir, "input")
	report := filepath.Join(dir, "report")
	_, err := runApply(t, "--input", in, "--report", report)
	want := "an elevated run only writes " + hosts.SystemPath()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if data, _ := os.ReadFile(report); strings.TrimSpace(string(data)) != want {
		t.Fatalf("report %q, want %q", data, want)
	}
}

// An elevated apply refuses --input and --report anywhere but the files
// input and report of one folder the elevator could have made, before it
// opens or creates either: no report appears and the hosts file is as it
// was.
func TestElevatedHostsApplyRefusesOtherInputAndReportOnWindows(t *testing.T) {
	me := mySID(t)
	cases := map[string]func(t *testing.T) (input, report string){
		"input alone": func(t *testing.T) (string, string) {
			return writeInput(t, helperDir(t), "input"), ""
		},
		"report alone": func(t *testing.T) (string, string) {
			return "", filepath.Join(helperDir(t), "report")
		},
		"another input name": func(t *testing.T) (string, string) {
			dir := helperDir(t)
			return writeInput(t, dir, "input.txt"), filepath.Join(dir, "report")
		},
		"another report name": func(t *testing.T) (string, string) {
			dir := helperDir(t)
			return writeInput(t, dir, "input"), filepath.Join(dir, "report.bat")
		},
		"report name in capitals": func(t *testing.T) (string, string) {
			dir := helperDir(t)
			return writeInput(t, dir, "input"), filepath.Join(dir, "REPORT")
		},
		"different folders": func(t *testing.T) (string, string) {
			return writeInput(t, helperDir(t), "input"), filepath.Join(helperDir(t), "report")
		},
		"a path with dot dot": func(t *testing.T) (string, string) {
			dir := helperDir(t)
			writeInput(t, dir, "input")
			via := filepath.Join(dir, "x") + `\..\` + filepath.Base(dir)
			return via + `\input`, via + `\report`
		},
		"a folder not named by the elevator": func(t *testing.T) (string, string) {
			dir := mkdirWithSDDL(t, "startup", "O:"+me+"D:P(A;OICI;FA;;;"+me+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
			return writeInput(t, dir, "input"), filepath.Join(dir, "report")
		},
		"a folder everyone can write in": func(t *testing.T) (string, string) {
			dir := mkdirWithSDDL(t, elevate.HelperDirPrefix+"open", "O:"+me+"D:P(A;OICI;FA;;;"+me+")(A;OICI;FA;;;WD)")
			return writeInput(t, dir, "input"), filepath.Join(dir, "report")
		},
	}
	for name, paths := range cases {
		t.Run(name, func(t *testing.T) {
			runElevated(t)
			input, report := paths(t)
			var args []string
			if input != "" {
				args = append(args, "--input", input)
			}
			if report != "" {
				args = append(args, "--report", report)
			}
			path, err := runApply(t, args...)
			if err == nil || err.Error() != errHelperFolder.Error() {
				t.Fatalf("err = %v, want %q", err, errHelperFolder)
			}
			if report != "" {
				if _, err := os.Lstat(report); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("report created: %v", err)
				}
			}
			if data, _ := os.ReadFile(path); string(data) != "127.0.0.1 localhost\n" {
				t.Fatalf("file modified:\n%s", data)
			}
		})
	}
}

// The elevated helper takes a folder whose owner is a user account and in
// which no one else but SYSTEM and Administrators can write, and nothing
// else. The owner can be another user than the one the helper runs as: a
// standard user who types an administrator's password runs it as that
// administrator.
func TestHelperDirSecurityOnWindows(t *testing.T) {
	me := mySID(t)
	other := "S-1-5-21-1-2-3-1001"
	trustedInstaller := "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	private := func(owner string) string {
		return "O:" + owner + "D:P(A;OICI;FA;;;" + owner + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	}
	for sddl, ok := range map[string]bool{
		private(me):    true,
		private(other): true,
		"O:S-1-12-1-1-2-3-4D:P(A;OICI;FA;;;S-1-12-1-1-2-3-4)":             true,
		private(me) + "(A;OICI;0x1200a9;;;BU)":                            true, // users may read
		private(me) + "(A;OICIIO;GA;;;CO)":                                true,
		private(me) + "(D;OICI;FA;;;WD)":                                  true,
		private("BA"):                                                     false,
		private("SY"):                                                     false,
		private(trustedInstaller):                                         false,
		private(me) + "(A;OICI;FA;;;WD)":                                  false,
		private(me) + "(A;CI;0x4;;;BU)":                                   false, // add a subfolder
		private(me) + "(A;;0x2;;;AU)":                                     false, // add a file
		private(me) + "(A;;0x40;;;BU)":                                    false, // delete a file
		private(me) + "(A;OICIIO;GW;;;BU)":                                false, // write what is made in it
		private(me) + "(A;;WD;;;BU)":                                      false, // change the access list
		private(me) + "(OA;;FA;bf967aba-0de6-11d0-a285-00aa003049e2;;WD)": false,
		"O:" + me + "D:NO_ACCESS_CONTROL":                                 false,
		"O:" + me:                                                         false,
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatalf("%s: %v", sddl, err)
		}
		if err := checkHelperDirSecurity(sd); (err == nil) != ok {
			t.Errorf("%s: err %v, want ok %v", sddl, err, ok)
		}
	}
}

// A folder in the Windows directory, Program Files or ProgramData is
// refused whoever owns it; the user's temp directory is not one of them.
func TestInSystemFolderOnWindows(t *testing.T) {
	for _, id := range systemFolders {
		path, err := windows.KnownFolderPath(id, 0)
		if err != nil {
			continue
		}
		root, err := finalPathOf(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{root, root + `\` + elevate.HelperDirPrefix + "x", strings.ToUpper(root) + `\a\b`} {
			if !inSystemFolder(p) {
				t.Errorf("inSystemFolder(%q) = false", p)
			}
		}
		if inSystemFolder(root + "x") {
			t.Errorf("inSystemFolder(%q) = true", root+"x")
		}
	}
	temp, err := finalPathOf(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if inSystemFolder(temp) {
		t.Errorf("inSystemFolder(%q) = true", temp)
	}
}
