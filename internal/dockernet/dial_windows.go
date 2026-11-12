package dockernet

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// busyWait is how long dial waits before trying a busy pipe again. x/sys has
// no WaitNamedPipe, and a short sleep that ends with ctx does the same job.
const busyWait = 20 * time.Millisecond

// dial connects to the daemon's named pipe at endpoint, such as
// \\.\pipe\docker_engine. The handle is opened for overlapped I/O, so
// os.NewFile hands it to the runtime's poller and deadlines work on it. A
// pipe whose every instance is in use is tried again until ctx ends. A pipe
// that is not there fails with ERROR_FILE_NOT_FOUND, which oserr counts as
// nothing listening. SECURITY_IDENTIFICATION stops the server at the other
// end from acting as this user.
func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(endpoint)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: endpoint, Err: err}
	}
	for {
		h, err := windows.CreateFile(name,
			windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			return &pipeConn{File: os.NewFile(uintptr(h), endpoint), addr: pipeAddr(endpoint)}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, &os.PathError{Op: "open", Path: endpoint, Err: err}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(busyWait):
		}
	}
}

// pipeConn is a client's end of a named pipe as a net.Conn. Reads, writes,
// Close and the deadlines are the file's.
type pipeConn struct {
	*os.File
	addr pipeAddr
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

// pipeAddr is a named pipe's path, as a net.Addr.
type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }
