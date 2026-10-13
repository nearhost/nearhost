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

	ctx, release := systemUp().notify(context.Background())
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

	_, release := systemUp().notify(context.Background())
	if !signal.Ignored(syscall.SIGPIPE) {
		t.Error("SIGPIPE is not ignored while up runs")
	}
	release()
	// The shutdown and the last lines come after the release.
	if !signal.Ignored(syscall.SIGPIPE) {
		t.Error("SIGPIPE is no longer ignored once up stops listening for signals")
	}
}
