//go:build unix

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Closing the terminal hangs up nearhost but not its ssh masters, which run
// in process groups of their own. up must shut down on it as on Ctrl-C.
func TestSystemUpShutsDownOnHangup(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("SIGHUP is ignored in this process (nohup), and stays so")
	}
	// If notify did not catch the signal it would end the test binary, so
	// catch it here as well for the length of the test.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	defer signal.Stop(guard)

	ctx, _, release := systemUp().notify(context.Background())
	defer release()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a hangup did not end the context up waits on")
	}
}

// A write to a closed pipe on standard output would otherwise end the process
// at once (`nearhost up | tee`, then Ctrl-C), before the shutdown has ended
// the ssh masters. With the signal ignored the write just fails.
func TestSystemUpIgnoresBrokenPipes(t *testing.T) {
	if signal.Ignored(syscall.SIGPIPE) {
		t.Skip("SIGPIPE is already ignored in this process")
	}
	defer signal.Reset(syscall.SIGPIPE) // the other tests keep the default

	_, _, release := systemUp().notify(context.Background())
	if !signal.Ignored(syscall.SIGPIPE) {
		t.Error("SIGPIPE is not ignored while up runs")
	}
	release()
	// The shutdown and the last lines come after the release.
	if !signal.Ignored(syscall.SIGPIPE) {
		t.Error("SIGPIPE is no longer ignored once up stops listening for signals")
	}
}

// A second signal cuts the shutdown short instead of ending the process. It
// must count however soon after the first it comes: up listens for both with
// one subscription, so there is no moment in which it is swallowed.
func TestSystemUpCountsASecondSignalRightAfterTheFirst(t *testing.T) {
	// Should notify miss a signal it would end the test binary, so catch
	// it here as well for the length of the test.
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	ctx, again, release := systemUp().notify(context.Background())
	defer signal.Reset(syscall.SIGPIPE)
	defer release()
	select {
	case <-again:
		t.Fatal("a second signal was reported before any was sent")
	case <-ctx.Done():
		t.Fatal("the context ended before any signal was sent")
	default:
	}
	// The second is sent the moment the first has been seen (two sent at
	// once may reach the process as one).
	for _, step := range []struct {
		seen <-chan struct{}
		lost string
	}{{ctx.Done(), "the first signal did not end the context"}, {again, "the second signal was swallowed"}} {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-step.seen:
		case <-time.After(5 * time.Second):
			t.Fatal(step.lost)
		}
	}
	// Later ones are absorbed, and the release ends the listening.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	release()
	release()
}

// A context that ends for another reason starts the shutdown too, and the
// signal that follows is then the one that hurries it.
func TestSystemUpHurriesOnTheFirstSignalAfterItsContextEnded(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	parent, cancel := context.WithCancel(context.Background())
	ctx, again, release := systemUp().notify(parent)
	defer signal.Reset(syscall.SIGPIPE)
	defer release()
	cancel()
	<-ctx.Done()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-again:
	case <-time.After(5 * time.Second):
		t.Fatal("the signal after the context ended did not hurry the shutdown")
	}
}
