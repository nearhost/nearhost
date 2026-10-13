// Package testutil holds helpers shared by tests. Only _test.go files import
// it.
package testutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FreePort returns a loopback TCP port that was unused a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// WaitUntil polls cond until it holds and fails the test after timeout.
func WaitUntil(t testing.TB, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// HTTPSClient returns a client that trusts only root and reaches alias by
// dialling 127.0.0.1:port, so no hosts entry is needed. Connections are not
// kept alive: each request sees the proxy's current configuration.
func HTTPSClient(t testing.TB, root *x509.Certificate, alias string, port int) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(root)
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: alias},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// ProxyPort returns a port free for both TCP and UDP: the proxy also listens
// for HTTP/3 on the UDP port matching its HTTPS port.
func ProxyPort(t testing.TB) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		port := FreePort(t)
		pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		pc.Close()
		return port
	}
	t.Fatal("no port free for both TCP and UDP")
	return 0
}

// ProxyPorts returns two different ports for a proxy's HTTPS and HTTP
// listeners. ProxyPort releases each port before the next is picked, so the
// same one can come back.
func ProxyPorts(t testing.TB) (httpsPort, httpPort int) {
	t.Helper()
	httpsPort = ProxyPort(t)
	for i := 0; i < 50; i++ {
		if httpPort = ProxyPort(t); httpPort != httpsPort {
			return httpsPort, httpPort
		}
	}
	t.Fatal("no second free port for the proxy")
	return 0, 0
}

// TempDir is t.TempDir with a short path (ssh control sockets have a length
// limit, and pgrep can take the path as a pattern) and a patient removal:
// Caddy issues certificates in the background and can still be writing one
// into its storage for a moment after it has stopped, which makes a single
// RemoveAll fail.
func TempDir(t testing.TB, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		WaitUntil(t, 10*time.Second, func() bool { return os.RemoveAll(dir) == nil }, "removal of "+dir)
	})
	return dir
}

// FakeSSH returns the path of the fake OpenSSH client kept with the transport
// tests. It records its arguments to $FAKE_SSH_LOG and is steered by the
// FAKE_SSH_* variables described in the script.
func FakeSSH(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the testutil package")
	}
	fake := filepath.Join(filepath.Dir(file), "..", "transport", "testdata", "fakessh", "ssh")
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	return fake
}

// Upstream starts a local HTTP server answering "<name>:<host>" and returns
// its port.
func Upstream(t testing.TB, name string) int {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name+":"+r.Host)
	}))
	t.Cleanup(up.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
