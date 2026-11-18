package tui

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// plainMethod is a method that needs no quotes on sh.
var plainMethod = regexp.MustCompile(`^[A-Z]+$`)

// errCurlUnsafe is the error for a request that holds a control character
// or bytes that are not UTF-8. Pasted raw, a control character can end a
// bracketed paste, erase the line or complete a file name, so such a
// request is not copied.
var errCurlUnsafe = errors.New("the request holds a control character or bytes that are not UTF-8")

// curlCommand is a curl command that makes a request again, for pasting
// into the shell of goos: sh on macOS and Linux, PowerShell on Windows.
// Nothing in the request runs on paste. On sh every value is one quoted
// argument. On PowerShell the request travels as a curl config file in a
// literal here-string, so no request data is a process argument, which
// Windows PowerShell 5.1 would split at a double quote. The body is sent as
// it is, and left out when bodyLeftOut. A request with a value that is not
// curlSafe on goos cannot be copied.
func curlCommand(goos string, method, url string, headers [][2]string, body []byte, bodyLeftOut bool) (string, error) {
	if bodyLeftOut {
		body = nil
	}
	values := []string{method, url, string(body)}
	for _, h := range headers {
		values = append(values, h[0], h[1])
	}
	for _, v := range values {
		if !curlSafe(goos, v) {
			return "", errCurlUnsafe
		}
	}
	if goos == "windows" {
		return "@'\n" + curlConfig(method, url, headers, body) + "'@ | curl.exe -K -", nil
	}
	return curlPOSIX(method, url, headers, body), nil
}

// curlSafe reports whether s can be pasted into the shell of goos: UTF-8
// with no control character other than line feed and carriage return, and
// tab on Windows only. No other C0 control, no DEL and no C1 control. On sh
// a raw tab completes a file name when the terminal does not bracket the
// paste, and closes the quote; PowerShell gets a tab escaped as \t in the
// config file.
func curlSafe(goos, s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		allowed := r == '\n' || r == '\r' || (r == '\t' && goos == "windows")
		if (r < 0x20 && !allowed) || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// curlPOSIX is the curl command for sh: each value in single quotes. A
// quote inside ends the string, adds an escaped quote and starts another.
func curlPOSIX(method, url string, headers [][2]string, body []byte) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	parts := []string{"curl"}
	switch method {
	case "HEAD":
		parts = append(parts, "-I", "--globoff")
	case "", "GET":
		parts = append(parts, "-i", "--globoff")
	default:
		m := method
		if !plainMethod.MatchString(m) {
			m = quote(m)
		}
		parts = append(parts, "-i", "--globoff", "-X", m)
	}
	parts = append(parts, quote(url))
	for _, h := range headers {
		parts = append(parts, "-H", quote(h[0]+": "+h[1]))
	}
	if len(body) > 0 {
		parts = append(parts, "--data-raw", quote(string(body)))
	}
	return strings.Join(parts, " ")
}

// curlConfig is the request as a curl config file, read with -K: one
// option a line, each value in double quotes. The escapes keep every value
// on its own line, so no line of the file can end a PowerShell here-string.
func curlConfig(method, url string, headers [][2]string, body []byte) string {
	var b strings.Builder
	line := func(option, value string) {
		b.WriteString(option + " = " + curlConfigQuote(value) + "\n")
	}
	line("url", url)
	switch method {
	case "", "GET":
	case "HEAD":
		b.WriteString("head\n")
	default:
		line("request", method)
	}
	for _, h := range headers {
		line("header", h[0]+": "+h[1])
	}
	if len(body) > 0 {
		line("data-raw", string(body))
	}
	b.WriteString("globoff\ninclude\n")
	return b.String()
}

// curlConfigEscapes are the escapes of a double-quoted curl config value.
var curlConfigEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

// curlConfigQuote is s as a double-quoted curl config value.
func curlConfigQuote(s string) string {
	return `"` + curlConfigEscapes.Replace(s) + `"`
}
