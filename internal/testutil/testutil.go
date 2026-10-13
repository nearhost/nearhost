// Package testutil holds helpers shared by tests. Only _test.go files import
// it.
package testutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
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
