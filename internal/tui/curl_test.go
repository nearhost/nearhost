package tui

import (
	"os/exec"
	"testing"
)

// curlCase is a request for curlCommand and the command it should give on
// each shell.
type curlCase struct {
	name        string
	method, url string
	headers     [][2]string
	body        string
	bodyLeftOut bool
	posix, win  string
}

// Each request comes out of curlCommand with every value one inert
// argument, quoted for the shell the command is pasted into.
func TestCurlCommand(t *testing.T) {
	for _, c := range []curlCase{{
		name: "a method with a backtick is quoted", method: "A`id`", url: "https://myapp.test/",
		posix: "curl -i --globoff -X 'A`id`' 'https://myapp.test/'",
		win:   "curl.exe -i --globoff -X 'A`id`' 'https://myapp.test/'",
	}, {
		name: "a plain method is not", method: "DELETE", url: "https://myapp.test/",
		posix: "curl -i --globoff -X DELETE 'https://myapp.test/'",
		win:   "curl.exe -i --globoff -X DELETE 'https://myapp.test/'",
	}, {
		name: "a path with quotes and a semicolon is one argument", method: "GET", url: "https://myapp.test/a';calc;'b",
		posix: `curl -i --globoff 'https://myapp.test/a'\'';calc;'\''b'`,
		win:   `curl.exe -i --globoff 'https://myapp.test/a'';calc;''b'`,
	}, {
		name: "a query with an ampersand", method: "GET", url: "https://myapp.test/?a=1&b=2",
		posix: "curl -i --globoff 'https://myapp.test/?a=1&b=2'",
		win:   "curl.exe -i --globoff 'https://myapp.test/?a=1&b=2'",
	}, {
		name: "a header value with a command substitution", method: "GET", url: "https://myapp.test/",
		headers: [][2]string{{"X-Name", "$(id)"}},
		posix:   "curl -i --globoff 'https://myapp.test/' -H 'X-Name: $(id)'",
		win:     "curl.exe -i --globoff 'https://myapp.test/' -H 'X-Name: $(id)'",
	}, {
		name: "a body that starts with @ is sent as it is", method: "POST", url: "https://myapp.test/", body: "@/etc/passwd",
		posix: "curl -i --globoff -X POST 'https://myapp.test/' --data-raw '@/etc/passwd'",
		win:   "curl.exe -i --globoff -X POST 'https://myapp.test/' --data-raw '@/etc/passwd'",
	}, {
		name: "a body with a newline", method: "POST", url: "https://myapp.test/", body: "a\nb'c",
		posix: "curl -i --globoff -X POST 'https://myapp.test/' --data-raw 'a\nb'\\''c'",
		win:   "curl.exe -i --globoff -X POST 'https://myapp.test/' --data-raw 'a\nb''c'",
	}, {
		name: "a body left out", method: "POST", url: "https://myapp.test/", body: "{", bodyLeftOut: true,
		posix: "curl -i --globoff -X POST 'https://myapp.test/'",
		win:   "curl.exe -i --globoff -X POST 'https://myapp.test/'",
	}, {
		name: "HEAD asks for the headers only", method: "HEAD", url: "https://myapp.test/",
		posix: "curl -I --globoff 'https://myapp.test/'",
		win:   "curl.exe -I --globoff 'https://myapp.test/'",
	}, {
		name: "brackets in a URL stay literal", method: "GET", url: "https://myapp.test/?ids[]=1&ids[]=2",
		posix: "curl -i --globoff 'https://myapp.test/?ids[]=1&ids[]=2'",
		win:   "curl.exe -i --globoff 'https://myapp.test/?ids[]=1&ids[]=2'",
	}, {
		name: "PowerShell's curly quotes are doubled too", method: "GET", url: "https://myapp.test/a\u2019;calc;\u2018b",
		posix: "curl -i --globoff 'https://myapp.test/a\u2019;calc;\u2018b'",
		win:   "curl.exe -i --globoff 'https://myapp.test/a\u2019\u2019;calc;\u2018\u2018b'",
	}} {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			want := c.posix
			if goos == "windows" {
				want = c.win
			}
			var body []byte
			if c.body != "" {
				body = []byte(c.body)
			}
			if got := curlCommand(goos, c.method, c.url, c.headers, body, c.bodyLeftOut); got != want {
				t.Errorf("%s on %s:\ngot  %s\nwant %s", c.name, goos, got, want)
			}
		}
	}
}

// curlProgram is the curl command's first word on goos, for tests that
// copy through the TUI on the system they run on.
func curlProgram(goos string) string {
	if goos == "windows" {
		return "curl.exe"
	}
	return "curl"
}

// sh reads a POSIX-quoted argument back as the one word it was given,
// whatever it holds.
func TestCurlQuoteReadsBackInSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh here")
	}
	for _, s := range []string{"a';calc;'b", "$(id)", "`id`", "a\nb", "a|b&c", "'", "''", "\\'"} {
		out, err := exec.Command(sh, "-c", `printf '%s|' `+curlQuote("linux", s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got := string(out); got != s+"|" {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}
