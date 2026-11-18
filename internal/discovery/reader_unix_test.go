//go:build unix

package discovery

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within runs read and fails the test binary when it has not returned by
// the deadline. A read that runs away on /dev/zero or waits on a FIFO is
// stopped there instead of filling memory or hanging the run.
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
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(2 * time.Second):
		panic(t.Name() + ": the read did not return; it read a device or waited on a FIFO")
	}
}

func TestDirReaderRefusesALinkToADevice(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	_, err := within(t, func() ([]byte, error) { return DirReader{Root: dir}.ReadFile(".env") })
	if err == nil || err.Error() != ".env is not a regular file" {
		t.Fatalf("err = %v", err)
	}
	_, err = Discover(DirReader{Root: dir}, Options{})
	if err == nil {
		t.Fatal("discovery should fail on a .env it cannot read")
	}
}

func TestDirReaderDoesNotOpenAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := within(t, func() ([]byte, error) { return DirReader{Root: dir}.ReadFile(".env") })
	if err == nil || err.Error() != ".env is not a regular file" {
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
