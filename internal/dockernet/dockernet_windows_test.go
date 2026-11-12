package dockernet

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// testPipe returns a pipe path no other test or process uses.
func testPipe(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return `\\.\pipe\nearhost-test-` + hex.EncodeToString(b)
}

// newPipe creates the one instance of a named pipe at path, open for
// overlapped I/O, and listening: a client can open it from now on.
func newPipe(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 0, nil)
	if err != nil {
		t.Fatalf("CreateNamedPipe: %v", err)
	}
	return h
}

// pipeDaemon serves body at /networks, once, on a new named pipe at path, as
// Docker Desktop's engine does on \\.\pipe\docker_engine. The pipe exists
// when it returns. Cleanup cancels whatever the server is still waiting on,
// so it never hangs.
func pipeDaemon(t *testing.T, path, body string) {
	t.Helper()
	h := newPipe(t, path)
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatalf("CreateEvent: %v", err)
	}
	// The wait for a client is done with an event before the handle goes to
	// os.NewFile, as the runtime's poller would not know this operation.
	ov := &windows.Overlapped{HEvent: ev}
	switch err := windows.ConnectNamedPipe(h, ov); err {
	case nil, windows.ERROR_PIPE_CONNECTED:
		windows.SetEvent(ev)
	case windows.ERROR_IO_PENDING:
	default:
		windows.CloseHandle(ev)
		windows.CloseHandle(h)
		t.Fatalf("ConnectNamedPipe: %v", err)
	}
	var f *os.File // set before served is sent on
	served := make(chan error, 1)
	go func() {
		served <- func() error {
			if _, err := windows.WaitForSingleObject(ev, windows.INFINITE); err != nil {
				return err
			}
			var n uint32
			if err := windows.GetOverlappedResult(h, ov, &n, false); err != nil && err != windows.ERROR_PIPE_CONNECTED {
				return err
			}
			f = os.NewFile(uintptr(h), path)
			req, err := http.ReadRequest(bufio.NewReader(f))
			if err != nil {
				return err
			}
			resp := &http.Response{
				StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1,
				Header:        http.Header{"Content-Type": {"application/json"}},
				ContentLength: int64(len(body)),
				Body:          io.NopCloser(strings.NewReader(body)),
				Request:       req,
			}
			if req.Method != http.MethodGet || req.URL.Path != "/networks" {
				resp.StatusCode, resp.ContentLength, resp.Body = http.StatusNotFound, 0, http.NoBody
			}
			if err := resp.Write(f); err != nil {
				return err
			}
			// Wait for the client to read it all before the pipe goes. A
			// client that has read it and gone breaks the pipe, which is fine.
			if err := windows.FlushFileBuffers(h); err != nil && err != windows.ERROR_BROKEN_PIPE {
				return err
			}
			return nil
		}()
	}()
	t.Cleanup(func() {
		windows.CancelIoEx(h, nil) // a wait for a client, or a read
		select {
		case err := <-served:
			if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				t.Errorf("pipe server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("pipe server did not stop")
			return // leave the handles to the server
		}
		if f != nil {
			f.Close()
		} else {
			windows.CloseHandle(h)
		}
		windows.CloseHandle(ev)
	})
}

func TestRangesFromANamedPipeOnWindows(t *testing.T) {
	pipe := testPipe(t)
	pipeDaemon(t, pipe, networks)
	t.Setenv("DOCKER_HOST", "npipe:///"+strings.ReplaceAll(pipe, `\`, "/"))
	if got := endpoints()[0]; got != pipe {
		t.Fatalf("first endpoint = %q, want %q", got, pipe)
	}
	got, err := Ranges(context.Background())
	if err != nil {
		t.Fatalf("Ranges: %v", err)
	}
	want := []string{"172.17.0.0/16", "192.168.97.0/24", "fd00:d0c::/64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %q, want %q", got, want)
	}
}

// The default pipes are left out, so a Docker Desktop running on the test
// machine does not answer in the missing pipe's place.
func TestRangesWithNoPipeFailsOnWindows(t *testing.T) {
	pipe := testPipe(t)
	t.Setenv("DOCKER_HOST", "npipe:///"+strings.ReplaceAll(pipe, `\`, "/"))
	if got := endpoints()[0]; got != pipe {
		t.Fatalf("first endpoint = %q, want %q", got, pipe)
	}
	begun := time.Now()
	got, err := rangesFrom(context.Background(), []string{pipe})
	if err == nil {
		t.Errorf("rangesFrom = %q with no error", got)
	} else if !strings.Contains(err.Error(), "no Docker daemon at "+pipe) {
		t.Errorf("rangesFrom error = %v, want one saying no daemon is there", err)
	}
	if took := time.Since(begun); took > 3*time.Second {
		t.Errorf("rangesFrom took %s", took)
	}
}

// A busy pipe is tried again until the context ends, not given up on.
func TestDialWaitsForABusyPipeOnWindows(t *testing.T) {
	pipe := testPipe(t)
	h := newPipe(t, pipe) // the only instance
	t.Cleanup(func() { windows.CloseHandle(h) })
	first, err := dial(context.Background(), pipe)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if c, err := dial(ctx, pipe); err == nil {
		c.Close()
		t.Fatal("dialled a second time while the only instance was in use")
	} else if err != context.DeadlineExceeded {
		t.Errorf("dial = %v, want %v", err, context.DeadlineExceeded)
	}
}

// TestEndpointsOnWindows checks the pipes looked for, in order, once each.
func TestEndpointsOnWindows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "npipe:////./pipe/host")
	sum := sha256.Sum256([]byte("desktop-linux"))
	meta := filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	if err := os.MkdirAll(filepath.Dir(meta), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(`{"currentContext":"desktop-linux"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte(`{"Name":"desktop-linux","Endpoints":{"docker":{"Host":"npipe:////./pipe/dockerDesktopLinuxEngine"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{`\\.\pipe\host`, `\\.\pipe\dockerDesktopLinuxEngine`, `\\.\pipe\docker_engine`}
	if got := endpoints(); !reflect.DeepEqual(got, want) {
		t.Errorf("endpoints() = %q, want %q", got, want)
	}

	// A tcp DOCKER_HOST is not on this machine, so only the defaults stay.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	t.Setenv("DOCKER_CONTEXT", "default")
	want = []string{`\\.\pipe\docker_engine`, `\\.\pipe\dockerDesktopLinuxEngine`}
	if got := endpoints(); !reflect.DeepEqual(got, want) {
		t.Errorf("endpoints() = %q, want %q", got, want)
	}
}
