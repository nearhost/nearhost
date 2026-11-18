package tui

import (
	"errors"
	"regexp"
	"strings"
)

// plainMethod is a method that needs no quotes on sh.
var plainMethod = regexp.MustCompile(`^[A-Z]+$`)

// errCurlNUL is the error for a request that holds a NUL byte, which no
// shell argument or curl config line can carry.
var errCurlNUL = errors.New("the request holds a NUL byte")

// curlCommand is a curl command that makes a request again, for pasting
// into the shell of goos: sh on macOS and Linux, PowerShell on Windows.
// Nothing in the request runs on paste. On sh every value is one quoted
// argument. On PowerShell the request travels as a curl config file in a
// literal here-string, so no request data is a process argument, which
// Windows PowerShell 5.1 would split at a double quote. The body is sent as
// it is, and left out when bodyLeftOut. A request that holds a NUL byte
// cannot be copied.
func curlCommand(goos string, method, url string, headers [][2]string, body []byte, bodyLeftOut bool) (string, error) {
	if bodyLeftOut {
		body = nil
	}
	if strings.ContainsRune(method+url, 0) || strings.ContainsRune(string(body), 0) {
		return "", errCurlNUL
	}
	for _, h := range headers {
		if strings.ContainsRune(h[0]+h[1], 0) {
			return "", errCurlNUL
		}
	}
	if goos == "windows" {
		return "@'\n" + curlConfig(method, url, headers, body) + "'@ | curl.exe -K -", nil
	}
	return curlPOSIX(method, url, headers, body), nil
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
