package firewall

import "testing"

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
