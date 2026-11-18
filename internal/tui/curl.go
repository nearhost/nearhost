package tui

import (
	"regexp"
	"strings"
)

// plainMethod is a method that needs no quotes on any shell.
var plainMethod = regexp.MustCompile(`^[A-Z]+$`)

// curlCommand is a curl command that makes a request again, for pasting
// into the shell of goos: sh on macOS and Linux, PowerShell on Windows.
// Every value is quoted as one argument, so nothing in a request runs on
// paste. The body is sent as it is, and left out when bodyLeftOut.
func curlCommand(goos string, method, url string, headers [][2]string, body []byte, bodyLeftOut bool) string {
	program := "curl"
	if goos == "windows" {
		program = "curl.exe"
	}
	parts := []string{program}
	switch method {
	case "HEAD":
		parts = append(parts, "-I", "--globoff")
	case "", "GET":
		parts = append(parts, "-i", "--globoff")
	default:
		m := method
		if !plainMethod.MatchString(m) {
			m = curlQuote(goos, m)
		}
		parts = append(parts, "-i", "--globoff", "-X", m)
	}
	parts = append(parts, curlQuote(goos, url))
	for _, h := range headers {
		parts = append(parts, "-H", curlQuote(goos, h[0]+": "+h[1]))
	}
	if len(body) > 0 && !bodyLeftOut {
		parts = append(parts, "--data-raw", curlQuote(goos, string(body)))
	}
	return strings.Join(parts, " ")
}

// powerShellQuotes are the characters PowerShell reads as a single quote.
// Inside a single-quoted string each one is written twice.
var powerShellQuotes = strings.NewReplacer(
	"'", "''", "\u2018", "\u2018\u2018", "\u2019", "\u2019\u2019", "\u201a", "\u201a\u201a", "\u201b", "\u201b\u201b",
)

// curlQuote quotes s as one literal argument for the shell of goos. Both
// wrap s in single quotes. On sh a quote inside ends the string, adds an
// escaped quote and starts a new one; on PowerShell it is written twice.
func curlQuote(goos, s string) string {
	if goos == "windows" {
		return "'" + powerShellQuotes.Replace(s) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
