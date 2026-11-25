package firewall

import (
	"testing"

	"golang.org/x/sys/windows"
)

// netsh gets the arguments as written, quotes and all, not re-quoted the
// way Go quotes a command line: netsh reads name="Nearhost" itself.
func TestNetshCommandLineKeepsQuotes(t *testing.T) {
	got := commandLine(`C:\Windows\System32\netsh.exe`, AddCommand(`C:\Program Files\nearhost\nearhost.exe`))
	want := `"C:\Windows\System32\netsh.exe" advfirewall firewall add rule name="Nearhost" dir=in action=allow program="C:\Program Files\nearhost\nearhost.exe" enable=yes profile=private,domain`
	if got != want {
		t.Errorf("commandLine =\n%s\nwant\n%s", got, want)
	}
}

// Asking netsh about a rule that is not there is not ready, and does not
// hang.
func TestReadyWithoutRule(t *testing.T) {
	out, err := Netsh(ShowCommand()...)
	if err == nil && HasProgram(out, `C:\nearhost-test-no-such\nearhost.exe`) {
		t.Error("a rule names a program that does not exist")
	}
}

// netsh writes to a pipe in the console's OEM code page: a path with an
// accented letter is read back as the same path.
func TestDecodeOEM(t *testing.T) {
	// Code page 850: 0x82 is é.
	got, err := decodeOEM(850, []byte("Programm: C:\\Users\\Ren\x82\\nearhost.exe\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Programm: C:\\Users\\René\\nearhost.exe\r\n"; string(got) != want {
		t.Errorf("decodeOEM = %q, want %q", got, want)
	}
	if !HasProgram(got, `C:\Users\René\nearhost.exe`) {
		t.Error("HasProgram missed the decoded path")
	}
	if got, err := decodeOEM(850, nil); err != nil || len(got) != 0 {
		t.Errorf("decodeOEM(nil) = %q, %v", got, err)
	}
}

// netsh, run by a core with no console, gets no window: CREATE_NO_WINDOW,
// as the codebase's other children, and its command line as written.
func TestNetshHasNoWindow(t *testing.T) {
	cmd := netshCmd(t.Context(), ShowCommand())
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("SysProcAttr = %+v, want CREATE_NO_WINDOW", cmd.SysProcAttr)
	}
	if want := commandLine(netshPath(), ShowCommand()); cmd.SysProcAttr.CmdLine != want {
		t.Errorf("CmdLine = %q, want %q", cmd.SysProcAttr.CmdLine, want)
	}
}
