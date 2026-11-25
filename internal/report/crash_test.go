package report

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoReportsPanicAsCrash(t *testing.T) {
	dir := t.TempDir()
	oldDir := CrashDir
	CrashDir = dir
	t.Cleanup(func() { CrashDir = oldDir })

	got := make(chan *Message, 1)
	ctx := WithSink(context.Background(), SinkFunc(func(m *Message) { got <- m }))
	Go(ctx, func() { panic("kaboom") })

	var m *Message
	select {
	case m = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("no crash message")
	}
	if m.Code != Crash || m.Level != Error || m.Title != "Nearhost stopped unexpectedly." {
		t.Fatalf("crash message = %+v", m)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "crash-*.log"))
	if len(files) != 1 {
		t.Fatalf("crash files = %v", files)
	}
	if !strings.Contains(m.Why, files[0]) {
		t.Fatalf("Why %q does not name %s", m.Why, files[0])
	}
	if m.Data["file"] != files[0] {
		t.Fatalf("data file = %v", m.Data["file"])
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "kaboom") {
		t.Fatalf("crash file lacks the panic value:\n%s", b)
	}
}

func TestGoWithoutPanicReportsNothing(t *testing.T) {
	c := &Collector{}
	done := make(chan struct{})
	Go(WithSink(context.Background(), c), func() { close(done) })
	<-done
	time.Sleep(10 * time.Millisecond)
	if len(c.Messages()) != 0 {
		t.Fatalf("messages = %v", c.Messages())
	}
}

func TestCrashFileHoldsStack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	oldVersion := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = oldVersion })

	stack := []byte("goroutine 7 [running]:\nmain.f()\n\t/src/main.go:10\n")
	path, err := CrashFile(dir, "index out of range", stack)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "crash-") || !strings.HasSuffix(path, ".log") {
		t.Fatalf("path = %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"1.2.3", runtime.GOOS + "/" + runtime.GOARCH, "index out of range", string(stack)} {
		if !strings.Contains(s, want) {
			t.Errorf("crash file lacks %q:\n%s", want, s)
		}
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(lineAfter(s, "Time: "))); err != nil {
		t.Errorf("no RFC 3339 time: %v\n%s", err, s)
	}

	// A second crash in the same second gets its own file.
	again, err := CrashFile(dir, "again", stack)
	if err != nil {
		t.Fatal(err)
	}
	if again == path {
		t.Fatal("the second crash overwrote the first")
	}
}

func lineAfter(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	return ""
}
