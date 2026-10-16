package proxy

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// runHelper does what a test runs this binary again to do, in a process of
// its own, so the test sees everything it writes, from Caddy's first load on.
func runHelper(what string) int {
	dir := os.Getenv("NEARHOST_PROXY_DIR")
	switch what {
	case "ensure-ca":
		if _, err := EnsureCA(dir); err != nil {
			fmt.Println(err)
			return 1
		}
	case "serve":
		https, _ := strconv.Atoi(os.Getenv("NEARHOST_PROXY_HTTPS"))
		http, _ := strconv.Atoi(os.Getenv("NEARHOST_PROXY_HTTP"))
		p := New(Options{StorageRoot: dir, HTTPSPort: https, HTTPPort: http})
		if err := p.Apply([]Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: "127.0.0.1:1"}}); err != nil {
			fmt.Println(err)
			return 1
		}
		// What a library writes through the standard log package, as
		// quic-go does when it cannot enlarge its UDP buffers on Linux.
		log.Print("failed to sufficiently increase receive buffer size")
		if err := p.Stop(); err != nil {
			fmt.Println(err)
			return 1
		}
	}
	return 0
}

// Caddy writes nothing to the terminal: not on its first load, when it
// would announce that its startup logger was redirected, not when the
// proxy serves and stops, and not for the libraries it uses, which write
// through the standard log package that Caddy sends to stderr. Whatever runs under the interface would otherwise
// print over it.
func TestCaddyWritesNothingToTheTerminal(t *testing.T) {
	for _, what := range []string{"ensure-ca", "serve"} {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "NEARHOST_PROXY_HELPER="+what, "NEARHOST_PROXY_DIR="+t.TempDir(),
			"NEARHOST_PROXY_HTTPS="+strconv.Itoa(freePort(t)), "NEARHOST_PROXY_HTTP="+strconv.Itoa(freePort(t)))
		var stderr, stdout bytes.Buffer
		cmd.Stderr, cmd.Stdout = &stderr, &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s%s", what, err, stdout.String(), stderr.String())
		}
		if stderr.Len() > 0 {
			t.Errorf("%s: Caddy wrote to stderr:\n%s", what, stderr.String())
		}
	}
}
