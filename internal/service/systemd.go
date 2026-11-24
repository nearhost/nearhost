package service

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// quoteUnit is s for the inside of double quotes in a unit file. systemd
// reads % as a specifier everywhere, and $ as a variable in ExecStart only.
func quoteUnit(s string, execStart bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "%", "%%"}
	if execStart {
		r = append(r, "$", "$$")
	}
	return strings.NewReplacer(r...).Replace(s)
}

// RenderUnit is d as a systemd user unit.
func RenderUnit(d Definition) []byte {
	var b bytes.Buffer
	// A core that fails at every start gives up after five tries in a minute
	// rather than being restarted for ever.
	b.WriteString("[Unit]\nDescription=Nearhost\nStartLimitIntervalSec=60\nStartLimitBurst=5\n")
	if d.Graphical {
		b.WriteString("After=graphical-session.target\n")
	}
	b.WriteString("\n[Service]\nExecStart=")
	for i, a := range d.command() {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, `"%s"`, quoteUnit(a, true))
	}
	b.WriteString("\nRestart=on-failure\nRestartSec=5\n")
	for _, e := range d.Env {
		fmt.Fprintf(&b, "Environment=\"%s\"\n", quoteUnit(e.Name+"="+e.Value, false))
	}
	if d.LogPath != "" {
		fmt.Fprintf(&b, "StandardError=append:%s\n", strings.ReplaceAll(d.LogPath, "%", "%%"))
	}
	target := "default.target"
	if d.Graphical {
		target = "graphical-session.target"
	}
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.Bytes()
}

// unquoteWords splits s into double-quoted words, undoing quoteUnit.
func unquoteWords(s string, execStart bool) ([]string, error) {
	var out []string
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out, nil
		}
		if s[0] != '"' {
			return nil, errors.New("an argument in ExecStart is not in double quotes")
		}
		var w strings.Builder
		i := 1
		for ; i < len(s) && s[i] != '"'; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			w.WriteByte(s[i])
		}
		if i >= len(s) {
			return nil, errors.New("a quote in ExecStart is not closed")
		}
		out = append(out, unspecify(w.String(), execStart))
		s = s[i+1:]
	}
}

// unspecify undoes the doubling of % and, in ExecStart, $.
func unspecify(s string, execStart bool) string {
	r := []string{"%%", "%"}
	if execStart {
		r = append(r, "$$", "$")
	}
	return strings.NewReplacer(r...).Replace(s)
}

// ParseUnit reads a unit RenderUnit could have written.
func ParseUnit(b []byte) (Definition, error) {
	var d Definition
	var argv []string
	haveExec := false
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch name {
		case "ExecStart":
			var err error
			if argv, err = unquoteWords(val, true); err != nil {
				return Definition{}, err
			}
			haveExec = true
		case "Environment":
			words, err := unquoteWords(val, false)
			if err != nil || len(words) != 1 {
				return Definition{}, errors.New("unreadable Environment line")
			}
			n, v, _ := strings.Cut(words[0], "=")
			d.Env = append(d.Env, EnvVar{n, v})
		case "StandardError":
			if p, ok := strings.CutPrefix(val, "append:"); ok {
				d.LogPath = unspecify(p, false)
			}
		case "WantedBy":
			d.Graphical = val == "graphical-session.target"
		}
	}
	if !haveExec {
		return Definition{}, errors.New("the unit has no ExecStart")
	}
	exe, args, ok := fromCommand(argv)
	if !ok {
		return Definition{}, errors.New("the unit does not start nearhost daemon --started-by service")
	}
	d.Exe, d.Args = exe, args
	return d, nil
}
