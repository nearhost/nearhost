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

// installSignals sets up signal handling as the real `up` does and undoes it
// when the test ends, so that no test sees what another one left: the
// subscription to the stop signals is released (which gives them back their
// former handling once the test's own guard is gone too), and SIGPIPE, which
// the production setup leaves ignored for good, is ignored afterwards only if
// it was before.
func installSignals(t *testing.T, parent context.Context) (ctx context.Context, again <-chan struct{}, release func()) {
	t.Helper()
	pipeIgnored := signal.Ignored(syscall.SIGPIPE)
	ctx, again, release = systemUp(nil).notify(parent)
	t.Cleanup(func() {
		release()
		if !pipeIgnored {
			// signal.Reset does not undo signal.Ignore; subscribing does,
			// and unsubscribing then leaves the signal as it is by default.
			c := make(chan os.Signal, 1)
			signal.Notify(c, syscall.SIGPIPE)
			signal.Stop(c)
		}
		if got := signal.Ignored(syscall.SIGPIPE); got != pipeIgnored {
			t.Errorf("SIGPIPE ignored = %v after the test, %v before it", got, pipeIgnored)
		}
	})
	return ctx, again, release
}

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

	ctx, _, _ := installSignals(t, context.Background())
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
		t.Skip("SIGPIPE was already ignored when this process started, and stays so")
	}

	_, _, release := installSignals(t, context.Background())
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

	ctx, again, release := installSignals(t, context.Background())
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
	// Wait until the guard has had all three. Nothing waits on the third
	// otherwise, and a signal still on its way when both subscriptions end
	// meets the default action and ends the test binary.
	for range 3 {
		select {
		case <-guard:
		case <-time.After(5 * time.Second):
			t.Fatal("the guard did not receive every signal sent")
		}
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
	ctx, again, _ := installSignals(t, parent)
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
