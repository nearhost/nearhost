package tui

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// curlCase is a request for curlCommand and what it should give: the
// command on sh, and the config file between the here-string markers on
// PowerShell.
type curlCase struct {
	name          string
	method, url   string
	headers       [][2]string
	body          string
	bodyLeftOut   bool
	posix, config string
}

// curlCases are requests whose values would run or change something on
// paste if they were not quoted.
var curlCases = []curlCase{{
	name: "a method with a backtick is quoted", method: "A`id`", url: "https://myapp.test/",
	posix:  "curl -i --globoff -X 'A`id`' 'https://myapp.test/'",
	config: "url = \"https://myapp.test/\"\nrequest = \"A`id`\"\ngloboff\ninclude\n",
}, {
	name: "a plain method is not", method: "DELETE", url: "https://myapp.test/",
	posix:  "curl -i --globoff -X DELETE 'https://myapp.test/'",
	config: "url = \"https://myapp.test/\"\nrequest = \"DELETE\"\ngloboff\ninclude\n",
}, {
	name: "a path with quotes and a semicolon is one argument", method: "GET", url: "https://myapp.test/a';calc;'b",
	posix:  `curl -i --globoff 'https://myapp.test/a'\'';calc;'\''b'`,
	config: "url = \"https://myapp.test/a';calc;'b\"\ngloboff\ninclude\n",
}, {
	name: "a query with an ampersand", method: "GET", url: "https://myapp.test/?a=1&b=2",
	posix:  "curl -i --globoff 'https://myapp.test/?a=1&b=2'",
	config: "url = \"https://myapp.test/?a=1&b=2\"\ngloboff\ninclude\n",
}, {
	name: "a header value with a command substitution", method: "GET", url: "https://myapp.test/",
	headers: [][2]string{{"X-Name", "$(id)"}},
	posix:   "curl -i --globoff 'https://myapp.test/' -H 'X-Name: $(id)'",
	config:  "url = \"https://myapp.test/\"\nheader = \"X-Name: $(id)\"\ngloboff\ninclude\n",
}, {
	name: "a header value with double quotes and a backslash", method: "GET", url: "https://myapp.test/",
	headers: [][2]string{{"X-Name", `x" -o C:\evil "`}},
	posix:   `curl -i --globoff 'https://myapp.test/' -H 'X-Name: x" -o C:\evil "'`,
	config:  "url = \"https://myapp.test/\"\nheader = \"X-Name: x\\\" -o C:\\\\evil \\\"\"\ngloboff\ninclude\n",
}, {
	name: "a body that starts with @ is sent as it is", method: "POST", url: "https://myapp.test/", body: "@/etc/passwd",
	posix:  "curl -i --globoff -X POST 'https://myapp.test/' --data-raw '@/etc/passwd'",
	config: "url = \"https://myapp.test/\"\nrequest = \"POST\"\ndata-raw = \"@/etc/passwd\"\ngloboff\ninclude\n",
}, {
	name: "a body with a newline", method: "POST", url: "https://myapp.test/", body: "a\nb'c",
	posix:  "curl -i --globoff -X POST 'https://myapp.test/' --data-raw 'a\nb'\\''c'",
	config: "url = \"https://myapp.test/\"\nrequest = \"POST\"\ndata-raw = \"a\\nb'c\"\ngloboff\ninclude\n",
}, {
	name: "a body that tries to end the here-string", method: "POST", url: "https://myapp.test/", body: "x\r\n'@ | calc\t",
	posix:  "curl -i --globoff -X POST 'https://myapp.test/' --data-raw 'x\r\n'\\''@ | calc\t'",
	config: "url = \"https://myapp.test/\"\nrequest = \"POST\"\ndata-raw = \"x\\r\\n'@ | calc\\t\"\ngloboff\ninclude\n",
}, {
	name: "a body left out", method: "POST", url: "https://myapp.test/", body: "{", bodyLeftOut: true,
	posix:  "curl -i --globoff -X POST 'https://myapp.test/'",
	config: "url = \"https://myapp.test/\"\nrequest = \"POST\"\ngloboff\ninclude\n",
}, {
	name: "HEAD asks for the headers only", method: "HEAD", url: "https://myapp.test/",
	posix:  "curl -I --globoff 'https://myapp.test/'",
	config: "url = \"https://myapp.test/\"\nhead\ngloboff\ninclude\n",
}, {
	name: "brackets in a URL stay literal", method: "GET", url: "https://myapp.test/?ids[]=1&ids[]=2",
	posix:  "curl -i --globoff 'https://myapp.test/?ids[]=1&ids[]=2'",
	config: "url = \"https://myapp.test/?ids[]=1&ids[]=2\"\ngloboff\ninclude\n",
}}

// Each request comes out of curlCommand with every value inert: one quoted
// argument on sh, a line of a curl config file in a literal here-string on
// PowerShell.
func TestCurlCommand(t *testing.T) {
	for _, c := range curlCases {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			want := c.posix
			if goos == "windows" {
				want = "@'\n" + c.config + "'@ | curl.exe -K -"
			}
			got, err := curlCommand(goos, c.method, c.url, c.headers, []byte(c.body), c.bodyLeftOut)
			if err != nil || got != want {
				t.Errorf("%s on %s:\ngot  %s (%v)\nwant %s", c.name, goos, got, err, want)
			}
		}
	}
}

// The PowerShell form spelled out whole.
func TestCurlCommandOnWindows(t *testing.T) {
	got, err := curlCommand("windows", "POST", "https://myapp.test/it's", [][2]string{{"Content-Type", "application/json"}}, []byte(`{"a":1}`), false)
	want := `@'
url = "https://myapp.test/it's"
request = "POST"
header = "Content-Type: application/json"
data-raw = "{\"a\":1}"
globoff
include
'@ | curl.exe -K -`
	if err != nil || got != want {
		t.Errorf("got  %s (%v)\nwant %s", got, err, want)
	}
}

// A request that holds a NUL byte cannot be copied on either shell.
func TestCurlCommandRefusesNUL(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, c := range []struct {
			url, header, body string
		}{{url: "https://myapp.test/a\x00b"}, {header: "a\x00b"}, {body: "a\x00b"}} {
			url := c.url
			if url == "" {
				url = "https://myapp.test/"
			}
			if got, err := curlCommand(goos, "POST", url, [][2]string{{"X-Name", c.header}}, []byte(c.body), false); err == nil {
				t.Errorf("%s copied a NUL as %q", goos, got)
			}
		}
	}
	if _, err := curlCommand("windows", "POST", "https://myapp.test/", nil, []byte("a\x00b"), true); err != nil {
		t.Errorf("a NUL in a body left out was refused: %v", err)
	}
}

// wantCurl is posix on macOS and Linux, and on Windows the PowerShell form
// with config, for tests that copy through the TUI on the system they run
// on.
func wantCurl(posix, config string) string {
	if runtime.GOOS == "windows" {
		return "@'\n" + config + "'@ | curl.exe -K -"
	}
	return posix
}

// sh reads a POSIX-quoted argument back as the one word it was given,
// whatever it holds.
func TestCurlQuoteReadsBackInSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh here")
	}
	for _, s := range []string{"a';calc;'b", "$(id)", "`id`", "a\nb", "a|b&c", "'", "''", "\\'"} {
		command, err := curlCommand("linux", "POST", "u", nil, []byte(s), false)
		if err != nil {
			t.Fatal(err)
		}
		quoted := command[strings.Index(command, "--data-raw ")+len("--data-raw "):]
		out, err := exec.Command(sh, "-c", `printf '%s|' `+quoted).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got := string(out); got != s+"|" {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}

// curl reads the PowerShell form's config file and sends each request
// exactly: method, path, header and body.
func TestCurlConfigReachesTheServer(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl here")
	}
	type seen struct {
		method, uri, header string
		body                []byte
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.RequestURI, r.Header.Get("X-Name"), body}
	}))
	defer srv.Close()
	for _, c := range []struct {
		method, path, header, body string
	}{
		{"A`id`", "/a';calc;'b?a=1&b=2", "$(id)", ""},
		{"POST", "/?ids[]=1&ids[]=2", `x" -o C:\evil "`, "@/etc/passwd"},
		{"PUT", "/", "'@", "a\nb'c\"d\\e\r\n'@ | calc\tz"},
		{"HEAD", "/head", "v", ""},
		{"GET", "/get", "v", ""},
	} {
		config := curlConfig(c.method, srv.URL+c.path, [][2]string{{"X-Name", c.header}}, []byte(c.body))
		cmd := exec.Command(curl, "--disable", "--silent", "--show-error", "--noproxy", "*", "--max-time", "10", "--output", "-", "-K", "-")
		cmd.Stdin = strings.NewReader(config)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("curl -K for %s %s: %v\n%s\nconfig:\n%s", c.method, c.path, err, out, config)
		}
		select {
		case s := <-got:
			if s.method != c.method || s.uri != c.path || s.header != c.header || !bytes.Equal(s.body, []byte(c.body)) {
				t.Errorf("sent %s %s X-Name %q body %q\nwant %s %s X-Name %q body %q",
					s.method, s.uri, s.header, s.body, c.method, c.path, c.header, c.body)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the server saw no %s %s", c.method, c.path)
		}
	}
}
