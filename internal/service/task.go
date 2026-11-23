package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf16"
)

// conhost is what runs nearhost without a console window: a logon task that
// ran it directly would open one for as long as the daemon lives.
const conhost = `%SystemRoot%\System32\conhost.exe`

// escapeArg is s as one argument of a Windows command line, the way
// syscall.EscapeArg writes it, which exists only on Windows.
func escapeArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
			b.WriteByte(c)
			continue
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// splitCommandLine is the reverse of joining escapeArg results.
func splitCommandLine(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var w strings.Builder
		inQuote := false
		for i < len(s) && (inQuote || (s[i] != ' ' && s[i] != '\t')) {
			slashes := 0
			for i < len(s) && s[i] == '\\' {
				slashes++
				i++
			}
			if i < len(s) && s[i] == '"' {
				w.WriteString(strings.Repeat(`\`, slashes/2))
				if slashes%2 == 1 {
					w.WriteByte('"')
				} else {
					inQuote = !inQuote
				}
				i++
				continue
			}
			w.WriteString(strings.Repeat(`\`, slashes))
			if i < len(s) && (inQuote || (s[i] != ' ' && s[i] != '\t')) {
				w.WriteByte(s[i])
				i++
			}
		}
		out = append(out, w.String())
	}
	return out
}

// RenderTask is d as Task Scheduler XML for user, in UTF-16LE with a byte
// order mark, which is what schtasks /Create /XML reads. Task Scheduler
// restarts a failed daemon after a minute, three times, never stops it for
// running on battery or for running a long time, and runs it at normal
// priority.
func RenderTask(d Definition, user string) []byte {
	args := slices.Concat([]string{"--headless"}, d.command())
	for i := range args {
		args[i] = escapeArg(args[i])
	}
	var x strings.Builder
	x.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	x.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	x.WriteString("  <Triggers>\n    <LogonTrigger>\n      <Enabled>true</Enabled>\n")
	x.WriteString("      <UserId>" + xmlText(user) + "</UserId>\n    </LogonTrigger>\n  </Triggers>\n")
	x.WriteString("  <Principals>\n    <Principal id=\"Author\">\n")
	x.WriteString("      <UserId>" + xmlText(user) + "</UserId>\n")
	x.WriteString("      <LogonType>InteractiveToken</LogonType>\n      <RunLevel>LeastPrivilege</RunLevel>\n")
	x.WriteString("    </Principal>\n  </Principals>\n")
	x.WriteString("  <Settings>\n    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	x.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	x.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	x.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	// Task Scheduler's default priority, 7, is below normal; 5 is normal.
	x.WriteString("    <Priority>5</Priority>\n")
	x.WriteString("    <RestartOnFailure>\n      <Interval>PT1M</Interval>\n      <Count>3</Count>\n    </RestartOnFailure>\n")
	x.WriteString("  </Settings>\n")
	x.WriteString("  <Actions Context=\"Author\">\n    <Exec>\n")
	x.WriteString("      <Command>" + xmlText(conhost) + "</Command>\n")
	x.WriteString("      <Arguments>" + xmlText(strings.Join(args, " ")) + "</Arguments>\n")
	x.WriteString("    </Exec>\n  </Actions>\n</Task>\n")

	u := utf16.Encode([]rune(x.String()))
	out := make([]byte, 0, 2+2*len(u))
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// taskXML is the part of a task that ParseTask reads.
type taskXML struct {
	Command   string `xml:"Actions>Exec>Command"`
	Arguments string `xml:"Actions>Exec>Arguments"`
}

// ParseTask reads a task RenderTask could have written, in UTF-16 or in
// UTF-8 (what PowerShell's Export-ScheduledTask prints), whatever its
// declaration says.
func ParseTask(b []byte) (Definition, error) {
	if len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF) {
		be := b[0] == 0xFE
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			if be {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			} else {
				u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
			}
		}
		b = []byte(string(utf16.Decode(u)))
	} else {
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	// The declaration may still say UTF-16; the text is UTF-8 by now.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var t taskXML
	if err := dec.Decode(&t); err != nil {
		return Definition{}, errors.New("not a Task Scheduler task: " + err.Error())
	}
	args := splitCommandLine(t.Arguments)
	if len(args) == 0 || args[0] != "--headless" {
		return Definition{}, errors.New("the task does not run nearhost through conhost --headless")
	}
	exe, rest, ok := fromCommand(args[1:])
	if !ok {
		return Definition{}, errors.New("the task does not start nearhost daemon --started-by service")
	}
	return Definition{Exe: exe, Args: rest}, nil
}
