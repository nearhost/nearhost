package documents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubFS is an FS whose Stat answers with err, or with nothing when err is
// nil.
type stubFS struct {
	LocalFS
	err error
}

// Stat returns f.err.
func (f stubFS) Stat(context.Context, string) (fs.FileInfo, error) { return nil, f.err }

// stubCloser records that it was closed.
type stubCloser struct{ closed atomic.Bool }

// Close records the close.
func (c *stubCloser) Close() error {
	c.closed.Store(true)
	return nil
}

// dialer hands out the FSs in fss in turn, with a closer for each, and
// counts its dials.
type dialer struct {
	mu      sync.Mutex
	fss     []FS
	closers []*stubCloser
	err     error
}

// dial is the pool's dial function.
func (d *dialer) dial(_ context.Context, server string) (FS, io.Closer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, nil, d.err
	}
	n := len(d.closers)
	fsys := FS(stubFS{})
	if n < len(d.fss) {
		fsys = d.fss[n]
	}
	c := &stubCloser{}
	d.closers = append(d.closers, c)
	return fsys, c, nil
}

// dials returns how many times dial was called.
func (d *dialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.closers)
}

func newTestPool(t *testing.T, d *dialer, idle time.Duration) *Pool {
	t.Helper()
	p := NewPool(d.dial, idle)
	t.Cleanup(func() { p.Close() })
	return p
}

func TestPoolReuses(t *testing.T) {
	d := &dialer{}
	p := newTestPool(t, d, time.Minute)
	ctx := context.Background()
	for range 2 {
		fsys, err := p.Open(ctx, "apollo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.Stat(ctx, "/x"); err != nil {
			t.Fatal(err)
		}
	}
	if n := d.dials(); n != 1 {
		t.Errorf("dialled %d times, want 1", n)
	}
}

func TestPoolLocal(t *testing.T) {
	d := &dialer{}
	p := newTestPool(t, d, time.Minute)
	fsys, err := p.Open(context.Background(), "local")
	if err != nil || fsys != (LocalFS{}) {
		t.Fatalf("Open(local) = %#v, %v; want LocalFS{}", fsys, err)
	}
	if n := d.dials(); n != 0 {
		t.Errorf("dialled %d times, want 0", n)
	}
}

func TestPoolRetriesADeadClient(t *testing.T) {
	d := &dialer{fss: []FS{stubFS{err: io.ErrUnexpectedEOF}, stubFS{}}}
	p := newTestPool(t, d, time.Minute)
	ctx := context.Background()
	fsys, err := p.Open(ctx, "apollo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(ctx, "/x"); err != nil {
		t.Fatalf("Stat = %v, want it to succeed on a fresh client", err)
	}
	if n := d.dials(); n != 2 {
		t.Errorf("dialled %d times, want 2", n)
	}
	if !d.closers[0].closed.Load() {
		t.Error("the dead client wasn't closed")
	}
	if d.closers[1].closed.Load() {
		t.Error("the fresh client was closed")
	}
	if _, err := fsys.Stat(ctx, "/x"); err != nil || d.dials() != 2 {
		t.Errorf("next Stat = %v after %d dials, want the fresh client kept", err, d.dials())
	}
}

func TestPoolKeepsClientOnAnswers(t *testing.T) {
	for _, answer := range []error{
		fmt.Errorf("stat /x: %w", fs.ErrNotExist),
		fmt.Errorf("stat /x: %w", fs.ErrPermission),
		context.Canceled,
	} {
		d := &dialer{fss: []FS{stubFS{err: answer}}}
		p := newTestPool(t, d, time.Minute)
		ctx := context.Background()
		fsys, err := p.Open(ctx, "apollo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.Stat(ctx, "/x"); !errors.Is(err, answer) {
			t.Errorf("Stat err = %v, want %v", err, answer)
		}
		if n := d.dials(); n != 1 || d.closers[0].closed.Load() {
			t.Errorf("%v: dialled %d times, closed %v; want the client kept", answer, n, d.closers[0].closed.Load())
		}
	}
}

func TestPoolNotConnected(t *testing.T) {
	d := &dialer{err: fmt.Errorf("%w: Nearhost isn't connected to apollo", ErrNotConnected)}
	p := newTestPool(t, d, time.Minute)
	if _, err := p.Open(context.Background(), "apollo"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	d.mu.Lock()
	d.err = nil
	d.mu.Unlock()
	if _, err := p.Open(context.Background(), "apollo"); err != nil {
		t.Fatalf("after connecting: %v", err)
	}
	if n := d.dials(); n != 1 {
		t.Errorf("dialled %d times, want 1: the failure must not be cached", n)
	}
}

func TestPoolIdle(t *testing.T) {
	d := &dialer{}
	p := newTestPool(t, d, 50*time.Millisecond)
	if _, err := p.Open(context.Background(), "apollo"); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); !d.closers[0].closed.Load(); {
		if time.Now().After(end) {
			t.Fatal("the idle client was never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := p.Open(context.Background(), "apollo"); err != nil || d.dials() != 2 {
		t.Errorf("Open after idle = %v with %d dials, want a fresh dial", err, d.dials())
	}
}

func TestPoolKeepsAnOpenFile(t *testing.T) {
	d := &dialer{}
	p := newTestPool(t, d, 50*time.Millisecond)
	ctx := context.Background()
	fsys, err := p.Open(ctx, "apollo")
	if err != nil {
		t.Fatal(err)
	}
	f, err := fsys.Open(ctx, "pool_test.go")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if d.closers[0].closed.Load() {
		t.Fatal("the client was closed while a file was open on it")
	}
	f.Close()
	for end := time.Now().Add(5 * time.Second); !d.closers[0].closed.Load(); {
		if time.Now().After(end) {
			t.Fatal("the idle client was never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPoolDropAndClose(t *testing.T) {
	d := &dialer{}
	p := NewPool(d.dial, time.Minute)
	ctx := context.Background()
	if _, err := p.Open(ctx, "apollo"); err != nil {
		t.Fatal(err)
	}
	p.Drop("apollo")
	if !d.closers[0].closed.Load() {
		t.Error("Drop didn't close the client")
	}
	if _, err := p.Open(ctx, "apollo"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if !d.closers[1].closed.Load() {
		t.Error("Close didn't close the client")
	}
	if _, err := p.Open(ctx, "apollo"); err == nil {
		t.Error("Open after Close worked")
	}
}
