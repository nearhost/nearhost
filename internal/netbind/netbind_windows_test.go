package netbind_test

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/oserr"
)

func TestListenSeesEveryHolderOnWindows(t *testing.T) {
	for _, h := range []struct{ name, network, addr string }{
		{"127.0.0.1", "tcp", "127.0.0.1:0"},
		{"[::1]", "tcp", "[::1]:0"},
		{"0.0.0.0", "tcp4", "0.0.0.0:0"},
	} {
		t.Run(h.name, func(t *testing.T) {
			holder, err := net.Listen(h.network, h.addr)
			if err != nil {
				if h.name == "[::1]" {
					t.Skipf("cannot hold %s: %v", h.addr, err)
				}
				t.Fatalf("cannot hold %s: %v", h.addr, err)
			}
			defer holder.Close()
			port := holder.Addr().(*net.TCPAddr).Port
			for _, addr := range []string{":" + strconv.Itoa(port), netbind.LoopbackProbe(port)} {
				l, err := netbind.Listen("tcp", addr)
				if err == nil {
					l.Close()
					t.Errorf("Listen(%q) succeeded beside a holder on %s", addr, h.addr)
				} else if !oserr.AddrInUse(err) {
					t.Errorf("Listen(%q) error = %v, want address in use", addr, err)
				}
			}
		})
	}
	t.Run("udp", func(t *testing.T) {
		holder, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Close()
		port := holder.LocalAddr().(*net.UDPAddr).Port
		pc, err := netbind.ListenPacket("udp", ":"+strconv.Itoa(port))
		if err == nil {
			pc.Close()
			t.Fatal("ListenPacket succeeded beside a holder on 127.0.0.1")
		}
		if !oserr.AddrInUse(err) {
			t.Fatalf("ListenPacket error = %v, want address in use", err)
		}
	})
}

func TestListenAfterServingIsFreeOnWindows(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})}
	t.Cleanup(func() { _ = srv.Close() })
	srv.SetKeepAlivesEnabled(false)
	go srv.Serve(l)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	srv.Close()

	deadline := time.Now().Add(time.Second)
	for {
		l, err := netbind.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			l.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port still not free a second after serving: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The question behind the proxy's TestWaitReleased: can something else hold
// 0.0.0.0:P while Nearhost binds 127.0.0.1:P and serves, unaware? Yes.
// Listen sets SO_EXCLUSIVEADDRUSE, but Windows lets a loopback bind succeed
// beside a plain wildcard holder (the more specific address wins, observed on
// windows-latest). This pins that fact; stage 8's gate must note that a
// wildcard holder of 443 is not seen.
func TestListenSucceedsBesideAWildcardHolderOnWindows(t *testing.T) {
	holder, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	port := holder.Addr().(*net.TCPAddr).Port
	l, err := netbind.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("Listen(127.0.0.1:%d) beside a wildcard holder failed: %v; Windows used to allow it", port, err)
	}
	l.Close()
}
