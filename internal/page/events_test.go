package page

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// streamLines starts GET /events on h and returns the lines it sends, and
// a func that ends the request.
func streamLines(t *testing.T, h http.Handler) (<-chan string, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines, cancel
}

// changes counts the data: changed lines on lines until d has passed.
func changes(lines <-chan string, d time.Duration) int {
	n := 0
	timeout := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return n
			}
			if l == "data: changed" {
				n++
			}
		case <-timeout:
			return n
		}
	}
}

func TestEventsSignal(t *testing.T) {
	// Unbuffered: each send returns once the stream has taken the signal,
	// so both land in one window with no wait between them.
	signal := make(chan struct{})
	stopped := make(chan struct{})
	h := New(Options{Core: newCore(), Watch: func() (<-chan struct{}, func()) {
		return signal, func() { close(stopped) }
	}})
	lines, cancel := streamLines(t, h)
	signal <- struct{}{}
	signal <- struct{}{}
	if n := changes(lines, 600*time.Millisecond); n != 1 {
		t.Fatalf("two signals in one window gave %d changes, want 1", n)
	}
	signal <- struct{}{}
	if n := changes(lines, 600*time.Millisecond); n != 1 {
		t.Fatalf("a third signal gave %d changes, want 1", n)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not stop watching when the request ended")
	}
}

func TestEventsKeepalive(t *testing.T) {
	before := keepalive
	keepalive = 50 * time.Millisecond
	t.Cleanup(func() { keepalive = before })
	lines, cancel := streamLines(t, New(Options{Core: newCore()}))
	defer cancel()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, ": keepalive") {
				return
			}
		case <-timeout:
			t.Fatal("no keepalive")
		}
	}
}

// The stream ends once Done is closed, so a page shut down leaves no
// request open.
func TestEventsEndOnDone(t *testing.T) {
	done := make(chan struct{})
	lines, cancel := streamLines(t, New(Options{Core: newCore(), Done: done}))
	defer cancel()
	close(done)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the stream outlived Done")
		}
	}
}
