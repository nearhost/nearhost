package oserr

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestAddrInUse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = net.Listen("tcp", l.Addr().String())
	if err == nil {
		t.Fatal("listening twice on one address succeeded")
	}
	if !AddrInUse(err) {
		t.Errorf("AddrInUse(%v) = false", err)
	}
	if AddrInUse(nil) || AddrInUse(io.EOF) {
		t.Error("AddrInUse is true for nil or io.EOF")
	}
}

func TestNothingListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	_, err = net.Dial("tcp", addr)
	if err == nil {
		t.Fatal("dialling a closed port succeeded")
	}
	if !NothingListening(err) {
		t.Errorf("NothingListening(%v) = false", err)
	}
	if NothingListening(io.EOF) {
		t.Error("NothingListening(io.EOF) = true")
	}
}

func TestNotAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(dir); !NotAFile(err) {
		t.Errorf("reading a directory: NotAFile(%v) = false", err)
	}
	if _, err := os.ReadFile(filepath.Join(file, "child")); !NotAFile(err) {
		t.Errorf("reading beneath a file: NotAFile(%v) = false", err)
	}
	if NotAFile(io.EOF) {
		t.Error("NotAFile(io.EOF) = true")
	}
}

func TestCrossDevice(t *testing.T) {
	if !CrossDevice(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: crossDeviceCode}) {
		t.Error("CrossDevice is false for the OS's cross-device code")
	}
	if CrossDevice(os.ErrPermission) {
		t.Error("CrossDevice(os.ErrPermission) = true")
	}
}

// Removing a file another handle has open is refused on Windows and allowed
// on Unix; either way, other errors are not InUse.
func TestInUse(t *testing.T) {
	name := filepath.Join(t.TempDir(), "held")
	if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Remove(name)
	f.Close()
	if refused := err != nil; InUse(err) != refused {
		t.Errorf("removing an open file returned %v; InUse = %v", err, InUse(err))
	}
	if _, statErr := os.Stat(name); err != nil && statErr != nil {
		t.Errorf("the refused removal removed the file: %v", statErr)
	}
	_, err = os.ReadFile(filepath.Join(t.TempDir(), "missing"))
	if InUse(err) || InUse(nil) || InUse(io.EOF) {
		t.Errorf("InUse is true for a missing file (%v), nil or io.EOF", err)
	}
}
