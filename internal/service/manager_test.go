package service

import (
	"context"
	"strings"
	"sync"
)

// fakeRunner records the commands run and answers each through handle, which
// may be nil for success with no output.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []string
	handle func(call string) ([]byte, error)
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.handle == nil {
		return nil, nil
	}
	return f.handle(call)
}

func (f *fakeRunner) reset() { f.calls = nil }

// testNames are not the real service's, so a test never touches the user's own.
var testNames = Names{"dev.nearhost.test", "nearhost-test.service", "NearhostTest"}
