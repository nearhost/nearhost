package oserr

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	// Beneath a regular file is ENOTDIR on Unix and ERROR_PATH_NOT_FOUND,
	// which is fs.ErrNotExist, on Windows.
	_, err := os.ReadFile(filepath.Join(file, "child"))
	if runtime.GOOS == "windows" {
		if NotAFile(err) || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("reading beneath a file: NotAFile(%v) = %v, want fs.ErrNotExist alone", err, NotAFile(err))
		}
	} else if !NotAFile(err) {
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
