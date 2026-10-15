package proxy

import (
	"log"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

// quietOnce primes Caddy's logging, once a process.
var quietOnce sync.Once

// quietCaddy keeps Caddy's start from reaching the terminal. Caddy starts out
// logging to stderr, and the first config that sends its logs elsewhere is
// announced through that first logger ("redirected default logger"), on the
// terminal the interface is drawing. A first config that keeps stderr, at
// error level, is no redirection, so nothing is announced; when the real
// config redirects, the announcement goes through the error-level logger and
// is dropped. Libraries that write through the standard log package, such
// as quic-go when it cannot enlarge its UDP buffers, reach the terminal
// too: Caddy sends that package to stderr when it starts. It is sent to the
// log sink instead, a line at a time, as Caddy's own lines are. Call it with
// caddyMu held, before the first caddy.Load.
func quietCaddy() {
	quietOnce.Do(func() {
		_ = caddy.Load([]byte(`{"admin":{"disabled":true,"config":{"persist":false}},`+
			`"logging":{"logs":{"default":{"writer":{"output":"stderr"},"level":"ERROR"}}}}`), false)
		log.SetFlags(0)
		log.SetPrefix("")
		log.SetOutput(stdLog{})
	})
}

// stdLog sends what is written through the standard log package to the log
// sink, a line at a time.
type stdLog struct{}

func (stdLog) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			emit("", line)
		}
	}
	return len(p), nil
}
