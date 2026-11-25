package firewall

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const testProgram = `C:\Program Files\nearhost\nearhost.exe`

// The rule lets in only the Nearhost program, and only on private and
// domain networks, never on a public one.
func TestAddCommandScopesToPrivateNetworks(t *testing.T) {
	want := []string{
		"advfirewall", "firewall", "add", "rule", `name="Nearhost"`, "dir=in", "action=allow",
		`program="C:\Program Files\nearhost\nearhost.exe"`, "enable=yes", "profile=private,domain",
	}
	if got := AddCommand(testProgram); !slices.Equal(got, want) {
		t.Errorf("AddCommand = %q, want %q", got, want)
	}
}

// showOutput is what netsh prints for a rule of program, in a language
// other than English: only the path is matched, never the labels.
func showOutput(program string) string {
	return "\r\nRegelname:                            Nearhost\r\n" +
		"----------------------------------------------------------------------\r\n" +
		"Aktiviert:                            Ja\r\n" +
		"Richtung:                             Eingehend\r\n" +
		"Programm:                             " + program + "\r\n" +
		"Aktion:                               Zulassen\r\n" +
		"OK.\r\n"
}

func TestHasProgram(t *testing.T) {
	for _, c := range []struct {
		name, out, program string
		want               bool
	}{
		{"same", showOutput(testProgram), testProgram, true},
		{"case differs", showOutput(`c:\program files\NEARHOST\nearhost.EXE`), testProgram, true},
		{"slashes and dots", showOutput(testProgram), `C:/Program Files/nearhost/./nearhost.exe`, true},
		{"another program", showOutput(`C:\old\nearhost.exe`), testProgram, false},
		{"a longer path ending the same", showOutput(`D:\x` + testProgram), testProgram, false},
		{"no rule", "No rules match the specified criteria.\r\n", testProgram, false},
		{"empty program", showOutput(testProgram), "", false},
	} {
		if got := HasProgram([]byte(c.out), c.program); got != c.want {
			t.Errorf("%s: HasProgram = %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeNetsh answers show with out (an error when out is ""), records every
// call, and fails the calls whose verb is in fail. An add that succeeds
// makes show print the added rule, unless lost is set; a delete empties it.
type fakeNetsh struct {
	out   string
	fail  map[string]bool
	lost  bool
	calls []string
}

func (f *fakeNetsh) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	verb := args[2]
	if f.fail[verb] {
		return []byte("netsh failed"), errors.New("exit status 1")
	}
	switch verb {
	case "show":
		if f.out == "" {
			return []byte("No rules match the specified criteria."), errors.New("exit status 1")
		}
		return []byte(f.out), nil
	case "delete":
		f.out = ""
	case "add":
		if !f.lost {
			for _, a := range args {
				if p, ok := strings.CutPrefix(a, "program="); ok {
					f.out = showOutput(strings.Trim(p, `"`))
				}
			}
		}
	}
	return []byte("Ok."), nil
}

func TestReadyWith(t *testing.T) {
	f := &fakeNetsh{out: showOutput(testProgram)}
	if !ReadyWith(f.run, testProgram) {
		t.Error("ReadyWith = false for the program's rule")
	}
	if want := []string{`advfirewall firewall show rule name="Nearhost" verbose`}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	if ReadyWith((&fakeNetsh{}).run, testProgram) {
		t.Error("ReadyWith = true with no rule")
	}
	if ReadyWith((&fakeNetsh{out: showOutput(`C:\old\nearhost.exe`)}).run, testProgram) {
		t.Error("ReadyWith = true for an old program's rule")
	}
}

func TestApplyWith(t *testing.T) {
	add := strings.Join(AddCommand(testProgram), " ")
	show := strings.Join(ShowCommand(), " ")
	del := strings.Join(DeleteCommand(), " ")
	for _, c := range []struct {
		name  string
		f     *fakeNetsh
		want  []string
		fails bool
	}{
		// netsh exits 1 both when there is no rule and when show itself
		// fails, in words of the system's language: a rule left by show's
		// failure is deleted too, so no second rule is added beside it.
		{"no rule yet", &fakeNetsh{}, []string{show, del, add, show}, false},
		{"show fails", &fakeNetsh{out: showOutput(`C:\old\nearhost.exe`), fail: map[string]bool{"show": true}}, []string{show, del, add, show}, true},
		{"already in place", &fakeNetsh{out: showOutput(testProgram)}, []string{show}, false},
		{"an old program's rule is deleted first", &fakeNetsh{out: showOutput(`C:\old\nearhost.exe`)}, []string{show, del, add, show}, false},
		{"add fails", &fakeNetsh{fail: map[string]bool{"add": true}}, []string{show, del, add}, true},
		{"delete of an old rule fails", &fakeNetsh{out: showOutput(`C:\old\nearhost.exe`), fail: map[string]bool{"delete": true}}, []string{show, del}, true},
		{"the added rule doesn't show", &fakeNetsh{lost: true}, []string{show, del, add, show}, true},
	} {
		err := ApplyWith(c.f.run, testProgram)
		if (err != nil) != c.fails {
			t.Errorf("%s: ApplyWith = %v", c.name, err)
		}
		if !slices.Equal(c.f.calls, c.want) {
			t.Errorf("%s: calls = %q, want %q", c.name, c.f.calls, c.want)
		}
	}
}

// A program path that could break out of netsh's quotes is refused before
// netsh runs.
func TestApplyWithRefusesQuote(t *testing.T) {
	f := &fakeNetsh{}
	if err := ApplyWith(f.run, `C:\a" dir=out "b.exe`); err == nil {
		t.Error("ApplyWith took a program with a quote")
	}
	if len(f.calls) != 0 {
		t.Errorf("netsh ran: %q", f.calls)
	}
}
