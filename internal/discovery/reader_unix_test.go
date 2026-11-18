//go:build unix

package discovery

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// within runs read and stops the test binary when it has not returned
// within 3 s, or when the heap passes 64 MiB while it runs. A read that
// runs away on /dev/zero or waits on a FIFO is stopped there instead of
// filling a shared machine's memory or hanging the run. The heap is checked
// every 5 ms, and io.ReadAll at most doubles its buffer between checks.
func within(t *testing.T, read func() ([]byte, error)) ([]byte, error) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := read()
		done <- result{data, err}
	}()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case r := <-done:
			return r.data, r.err
		case <-deadline:
			panic(t.Name() + ": the read did not return; it read a device or waited on a FIFO")
		case <-tick.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > 64<<20 {
				panic(t.Name() + ": the read passed 64 MiB; it read a device")
			}
		}
	}
}

func TestDirReaderRefusesALinkToADevice(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	_, err := within(t, func() ([]byte, error) { return DirReader{Root: dir}.ReadFile(".env") })
	if err == nil || err.Error() != ".env is not a regular file, so Nearhost did not read it." {
		t.Fatalf("err = %v", err)
	}
	_, err = within(t, func() ([]byte, error) {
		_, err := Discover(DirReader{Root: dir}, Options{})
		return nil, err
	})
	if err == nil || err.Error() != ".env is not a regular file, so Nearhost did not read it." {
		t.Fatalf("Discover: err = %v", err)
	}
}

func TestDirReaderDoesNotOpenAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := within(t, func() ([]byte, error) { return DirReader{Root: dir}.ReadFile(".env") })
	if err == nil || err.Error() != ".env is not a regular file, so Nearhost did not read it." {
		t.Fatalf("err = %v", err)
	}
}

// A monorepo links a project's files to shared ones; they are still read.
func TestDirReaderReadsALinkToARegularFile(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared.env")
	if err := os.WriteFile(shared, []byte("APP_PORT=8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	r := DirReader{Root: dir}
	data, err := r.ReadFile(".env")
	if err != nil || string(data) != "APP_PORT=8080\n" {
		t.Fatalf("ReadFile = %q %v", data, err)
	}
	if ok, err := r.Exists(".env"); err != nil || !ok {
		t.Errorf("Exists = %v %v", ok, err)
	}
}
