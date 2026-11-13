package transport

import (
	"os"
	"strings"
	"testing"
	"time"
)

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

// logLines returns the argv lines the fake ssh recorded.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(readLog(t, path)), "\n")
}
