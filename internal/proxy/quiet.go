package proxy

import (
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
// is dropped. Call it with caddyMu held, before the first caddy.Load.
func quietCaddy() {
	quietOnce.Do(func() {
		_ = caddy.Load([]byte(`{"admin":{"disabled":true,"config":{"persist":false}},`+
			`"logging":{"logs":{"default":{"writer":{"output":"stderr"},"level":"ERROR"}}}}`), false)
	})
}
