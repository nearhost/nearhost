//go:build unix

package engine

import (
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// Unix only: the ControlMaster fake leaves a forward's local port free, so
// the test can listen there, first as the app the forward reaches and then
// as another program that took the port while the server reconnects. Every
// OS: TestSiteOfADegradedProjectIsDown checks the site the proxy is given,
// and TestPerProcessSiteDialsNothingWhileItsForwardIsDown the dialling.
func TestSiteDialsNothingWhileItsServerReconnects(t *testing.T) {
	if perProcessTransport() {
		t.Skip("the per-process fake holds the forward's port itself")
	}
	ssh, flag := downWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = ssh })
	applied := recordApplied(h)
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	mustStart(t, h, "myapp")
	port := h.eng.Status("myapp").WebLocalPort
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	var conns, requests atomic.Int32
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			io.WriteString(w, "the app")
		}),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				conns.Add(1)
			}
		},
	}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	if code, body := h.getEventually(t, "myapp.test", "/"); code != 200 || body != "the app" {
		t.Fatalf("running: %d %q", code, body)
	}

	// The connection drops and stays down: the port is free, and whatever
	// listens there now must get nothing.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	waitState(t, h, "myapp", StateDegraded)
	// A reload drops the connections that arrive while it swaps the routes,
	// so the requests wait for the one that takes the site down. The gate
	// shut before it, at the drop.
	testutil.WaitUntil(t, 10*time.Second, func() bool { down, _ := applied.lastOf("myapp", 80); return down }, "the proxy to get the site down")
	c0, r0 := conns.Load(), requests.Load()
	for range 3 {
		if code, body := h.get(t, "myapp.test", "/secret?token=1"); code != 502 || body != "" {
			t.Fatalf("degraded: %d %q", code, body)
		}
	}
	if c, r := conns.Load()-c0, requests.Load()-r0; c != 0 || r != 0 {
		t.Fatalf("while the server reconnected, the listener on the forward's port got %d connections and %d requests", c, r)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, "myapp", StateRunning)
	if code, body := h.getEventually(t, "myapp.test", "/"); code != 200 || body != "the app" {
		t.Fatalf("reconnected: %d %q", code, body)
	}
}
