package transport

import (
	"io"
	"os"
	"os/exec"
	"time"
)

// stderrDrainWait bounds how long the end of a long-lived ssh waits, once
// the process has been reaped, for the last of its stderr to be read. Lines
// ssh wrote just before it exited are still in the pipe then, and say why
// it exited: a refused login among them. Without a child holding the pipe
// the read ends at once; a ProxyCommand or ProxyJump child that inherited it
// can hold it for as long as it lives, and is waited for this long only.
const stderrDrainWait = 300 * time.Millisecond

// stderrCloseWait bounds how long the end of an ssh's stderr waits, once its
// read end is closed, for the copy to hand on what it read last.
const stderrCloseWait = 100 * time.Millisecond

// stderrPipe carries a long-lived ssh's stderr to a writer through a pipe
// of its own, read by a goroutine of its own. ssh writes to an *os.File, so
// exec starts no copying goroutine and cmd.Wait returns as soon as the
// process is reaped, whoever else holds the pipe. Process exit and the end
// of its stderr are two events: the supervisor learns of the first at once,
// and reads the second only to say why.
type stderrPipe struct {
	r, w *os.File
	// read closes when the copy has ended: every holder of the write end
	// has closed it, or the read end was closed (see end).
	read chan struct{}
	// done closes once end has finished: stderr has been read to its end,
	// or for stderrDrainWait after the process was reaped.
	done chan struct{}
}

// pipeStderr gives cmd a pipe as its stderr and starts copying it to to.
// Call started after cmd.Start, whether or not that succeeded.
func pipeStderr(cmd *exec.Cmd, to io.Writer) (*stderrPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = w
	p := &stderrPipe{r: r, w: w, read: make(chan struct{}), done: make(chan struct{})}
	go copyStderr(r, to, p.read)
	return p, nil
}

// copyStderr copies ssh's stderr until the pipe ends or its read end is
// closed, then closes read.
func copyStderr(r *os.File, to io.Writer, read chan struct{}) {
	_, _ = io.Copy(to, r)
	close(read)
}

// started closes this process's copy of the write end, once ssh has its
// own: from then on the pipe ends when ssh, and any child that inherited
// it, have closed theirs. After a failed start nothing else holds it, so
// the copy ends at once.
func (p *stderrPipe) started() {
	_ = p.w.Close()
}

// end waits at most wait for the copy to reach the end of the pipe, then
// closes the read end, which ends the copy even while a child of ssh still
// holds the write end: on Unix at once, and on Windows by cancelling the
// read under way (os.File.Close calls CancelIoEx on a pipe). It then waits
// for the copy to finish, so the last chunk it read reaches the writer: a
// "Permission denied" lost there would turn a refused login into a quick
// retry. That wait is bounded too, by stderrCloseWait: should a close ever
// fail to end a read, the caller goes on with what was read, and the copy
// and the close, which hold only the read end, end by themselves when the
// read does. It closes done.
func (p *stderrPipe) end(wait time.Duration) {
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-p.read:
		case <-t.C:
		}
		t.Stop()
	}
	// Close waits for a read under way to return; it runs on its own so
	// that even a close that never returned would not hold the caller.
	go p.r.Close()
	t := time.NewTimer(stderrCloseWait)
	select {
	case <-p.read:
	case <-t.C:
	}
	t.Stop()
	close(p.done)
}
