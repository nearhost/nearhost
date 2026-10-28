package documents

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

// localServer is the server name of the laptop itself.
const localServer = "local"

// errPoolClosed is returned by a pool after Close.
var errPoolClosed = errors.New("the documents pool is closed")

// Pool keeps one file system client per server, so serving a tree's files
// doesn't open a fresh SFTP session for every request. A client unused for
// the idle time is closed.
type Pool struct {
	dial func(ctx context.Context, server string) (FS, io.Closer, error)
	idle time.Duration

	mu      sync.Mutex
	clients map[string]*pooled
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

// pooled is one server's client. ready is closed once its dial has
// finished, and fs, closer and err are set before that. busy counts the
// calls and open files using it, which keep it from being closed as idle.
type pooled struct {
	ready  chan struct{}
	fs     FS
	closer io.Closer
	err    error
	busy   int
	used   time.Time
}

// NewPool returns a pool that opens clients with dial and closes one unused
// for idle. A zero or negative idle keeps clients until they are dropped.
func NewPool(dial func(ctx context.Context, server string) (FS, io.Closer, error), idle time.Duration) *Pool {
	p := &Pool{
		dial:    dial,
		idle:    idle,
		clients: map[string]*pooled{},
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if idle > 0 {
		go p.reap()
	} else {
		close(p.done)
	}
	return p
}

// Open returns the FS for server, dialling it unless a client is cached. The
// laptop's own, server "local", is LocalFS without dialling. The FS it
// returns goes through the pool on every call: a call that fails for a
// reason other than the server's answer (a missing file, say) drops the
// client and is tried once more on a fresh one, so a client that died while
// cached costs nothing. Open is an Opener.
func (p *Pool) Open(ctx context.Context, server string) (FS, error) {
	if server == localServer {
		return LocalFS{}, nil
	}
	c, err := p.acquire(ctx, server)
	if err != nil {
		return nil, err
	}
	p.release(c)
	return pooledFS{p: p, server: server}, nil
}

// acquire returns server's client, dialling it when there is none, and
// marks it busy until release. Callers waiting for one dial share it. A
// dial that failed is not cached.
func (p *Pool) acquire(ctx context.Context, server string) (*pooled, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
		c := p.clients[server]
		if c == nil {
			c = &pooled{ready: make(chan struct{}), busy: 1}
			p.clients[server] = c
			p.mu.Unlock()
			return p.dialInto(ctx, server, c)
		}
		c.busy++
		p.mu.Unlock()
		select {
		case <-c.ready:
		case <-ctx.Done():
			p.release(c)
			return nil, ctx.Err()
		}
		if c.err == nil {
			return c, nil
		}
		p.release(c)
		// Another caller's dial failed. Its cancellation isn't this
		// caller's, so this one dials for itself.
		if !errors.Is(c.err, context.Canceled) && !errors.Is(c.err, context.DeadlineExceeded) {
			return nil, c.err
		}
	}
}

// dialInto dials server for c, which acquire has just put in the pool.
func (p *Pool) dialInto(ctx context.Context, server string, c *pooled) (*pooled, error) {
	fsys, closer, err := p.dial(ctx, server)
	p.mu.Lock()
	c.fs, c.closer, c.err, c.used = fsys, closer, err, time.Now()
	if err == nil && p.closed {
		c.err = errPoolClosed
	}
	if c.err != nil {
		c.busy--
		if p.clients[server] == c {
			delete(p.clients, server)
		}
	}
	close(c.ready)
	p.mu.Unlock()
	if c.err != nil {
		if err == nil {
			closer.Close()
		}
		return nil, c.err
	}
	return c, nil
}

// release marks the end of one use of c.
func (p *Pool) release(c *pooled) {
	p.mu.Lock()
	c.busy--
	c.used = time.Now()
	p.mu.Unlock()
}

// drop closes c and forgets it as server's client, unless another caller
// has already replaced it.
func (p *Pool) drop(server string, c *pooled) {
	p.mu.Lock()
	if p.clients[server] != c {
		p.mu.Unlock()
		return
	}
	delete(p.clients, server)
	p.mu.Unlock()
	c.closer.Close()
}

// Drop closes and forgets server's client. A dial still under way is left to
// finish.
func (p *Pool) Drop(server string) {
	p.mu.Lock()
	c := p.clients[server]
	p.mu.Unlock()
	if c == nil {
		return
	}
	select {
	case <-c.ready:
		if c.err == nil {
			p.drop(server, c)
		}
	default:
	}
}

// Close stops closing idle clients and closes every client. Open fails after
// it. A dial still under way closes its client when it finishes.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	var closers []io.Closer
	for server, c := range p.clients {
		select {
		case <-c.ready:
			closers = append(closers, c.closer)
			delete(p.clients, server)
		default:
		}
	}
	p.mu.Unlock()
	close(p.stop)
	<-p.done
	var errs []error
	for _, c := range closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// reap closes idle clients, checking every half idle time, until Close.
func (p *Pool) reap() {
	defer close(p.done)
	tick := time.NewTicker(p.idle / 2)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-tick.C:
			p.closeIdle(now)
		}
	}
}

// closeIdle closes every client that nothing has used since idle before
// now.
func (p *Pool) closeIdle(now time.Time) {
	var closers []io.Closer
	p.mu.Lock()
	for server, c := range p.clients {
		select {
		case <-c.ready:
		default:
			continue
		}
		if c.busy == 0 && now.Sub(c.used) >= p.idle {
			closers = append(closers, c.closer)
			delete(p.clients, server)
		}
	}
	p.mu.Unlock()
	for _, c := range closers {
		c.Close()
	}
}

// pooledFS is the FS the pool hands out for one server. Each call goes to
// the server's current client.
type pooledFS struct {
	p      *Pool
	server string
}

// RealPath resolves p's symlinks on the server.
func (f pooledFS) RealPath(ctx context.Context, p string) (string, error) {
	return use(ctx, f, func(fsys FS) (string, error) { return fsys.RealPath(ctx, p) }, nil)
}

// Stat returns the file information for p on the server.
func (f pooledFS) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	return use(ctx, f, func(fsys FS) (fs.FileInfo, error) { return fsys.Stat(ctx, p) }, nil)
}

// Open opens p on the server. The client stays busy, so it isn't closed as
// idle, until the file is closed.
func (f pooledFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	return use(ctx, f, func(fsys FS) (io.ReadCloser, error) { return fsys.Open(ctx, p) },
		func(r io.ReadCloser, release func()) io.ReadCloser {
			return &heldFile{ReadCloser: r, release: release}
		})
}

// Home returns the user's home folder on the server.
func (f pooledFS) Home(ctx context.Context) (string, error) {
	return use(ctx, f, func(fsys FS) (string, error) { return fsys.Home(ctx) }, nil)
}

// use runs call on the server's client. When call fails for a reason that
// isn't the server's answer, the client is dropped and call runs once more
// on a fresh one. hold, when set, wraps a successful result and takes over
// releasing the client.
func use[T any](ctx context.Context, f pooledFS, call func(FS) (T, error), hold func(T, func()) T) (T, error) {
	var zero T
	for try := 0; ; try++ {
		c, err := f.p.acquire(ctx, f.server)
		if err != nil {
			return zero, err
		}
		v, err := call(c.fs)
		if err == nil && hold != nil {
			var once sync.Once
			return hold(v, func() { once.Do(func() { f.p.release(c) }) }), nil
		}
		f.p.release(c)
		if err == nil || try > 0 || !brokenClient(ctx, err) {
			return v, err
		}
		f.p.drop(f.server, c)
	}
}

// brokenClient reports whether err, from a call made under ctx, says the
// client itself failed, such as an SFTP session whose ssh died. An answer
// from the server (a missing file, a path through a file, no permission, any
// SFTP status), a server that isn't connected, and the caller's own
// cancellation leave the client be.
func brokenClient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var status *sftp.StatusError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrNotConnected), errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.ELOOP), errors.As(err, &status):
		return false
	}
	return true
}

// heldFile is a file open on a pooled client, which it keeps busy until it is
// closed.
type heldFile struct {
	io.ReadCloser
	release func()
}

// Close closes the file and releases its client.
func (h *heldFile) Close() error {
	defer h.release()
	return h.ReadCloser.Close()
}

// Compile-time checks that the pool's FS is an FS and Open is an Opener.
var (
	_ FS     = pooledFS{}
	_ Opener = (*Pool)(nil).Open
)
