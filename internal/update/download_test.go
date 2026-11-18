package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zipOf is a zip archive of the named files.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// release serves a release of version holding archive as the windows/amd64
// zip, with sums as its checksums.txt; "" means the archive's real checksum.
func release(t *testing.T, version string, archive []byte, sums string) Downloader {
	t.Helper()
	asset := "nearhost_" + version + "_windows_amd64.zip"
	if sums == "" {
		sum := sha256.Sum256(archive)
		sums = "aaaa  nearhost_" + version + "_linux_amd64.tar.gz\n" + hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v" + version + "/checksums.txt":
			fmt.Fprint(w, sums)
		case "/v" + version + "/" + asset:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Downloader{BaseURL: srv.URL, GOARCH: "amd64"}
}

// folder is an install folder with a space and non-ASCII letters in its name.
func folder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "José Núñez", "nearhost bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// only fails unless dir holds exactly the named files: a failed download
// must leave nothing behind.
func only(t *testing.T, dir string, names ...string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("%s holds %v, want %v", dir, got, names)
	}
}

func TestDownloadPutsTheReleasesBinaryAtExe(t *testing.T) {
	d := release(t, "0.13.0-rc.2", zipOf(t, map[string]string{"LICENSE": "MIT", "nearhost.exe": "new binary", "README.md": "x"}), "")
	exe := filepath.Join(folder(t), "nh.exe") // a renamed copy is updated where it is
	if err := d.Fetch(context.Background(), "v0.13.0-rc.2", exe); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "new binary" {
		t.Fatalf("exe holds %q, err %v", got, err)
	}
	only(t, filepath.Dir(exe), "nh.exe")
}

func TestDownloadRefusesAWrongChecksum(t *testing.T) {
	const version = "0.13.0"
	sums := strings.Repeat("0", 64) + "  nearhost_" + version + "_windows_amd64.zip\n"
	d := release(t, version, zipOf(t, map[string]string{"nearhost.exe": "tampered"}), sums)
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Fetch(context.Background(), version, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err %v, want a checksum error", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWithoutAChecksumLine(t *testing.T) {
	const version = "0.13.0"
	d := release(t, version, zipOf(t, map[string]string{"nearhost.exe": "x"}), "aaaa  nearhost_0.13.0_linux_amd64.tar.gz\n")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Fetch(context.Background(), version, exe)
	if err == nil || !strings.Contains(err.Error(), "nearhost_0.13.0_windows_amd64.zip") {
		t.Fatalf("err %v, want it to name the missing archive", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWhenTheReleaseIsMissing(t *testing.T) {
	d := release(t, "0.13.0", nil, "")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Fetch(context.Background(), "0.14.0", exe)
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "/v0.14.0/checksums.txt") {
		t.Fatalf("err %v, want the address and the status", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWhenTheArchiveHasNoBinary(t *testing.T) {
	d := release(t, "0.13.0", zipOf(t, map[string]string{"README.md": "x"}), "")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Fetch(context.Background(), "0.13.0", exe)
	if err == nil || !strings.Contains(err.Error(), "no nearhost.exe") {
		t.Fatalf("err %v", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadStopsWhenCancelled(t *testing.T) {
	d := release(t, "0.13.0", zipOf(t, map[string]string{"nearhost.exe": "x"}), "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exe := filepath.Join(folder(t), "nearhost.exe")
	if err := d.Fetch(ctx, "0.13.0", exe); err == nil {
		t.Fatal("a cancelled download succeeded")
	}
	only(t, filepath.Dir(exe))
}

// staleFile leaves a file at path, as an earlier update that failed might
// have.
func staleFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file at the download's path from an earlier update is replaced by what
// this one wrote and checked.
func TestFetchReplacesAStaleFile(t *testing.T) {
	d := release(t, "0.13.0", zipOf(t, map[string]string{"nearhost.exe": "new binary"}), "")
	dest := filepath.Join(folder(t), "nearhost.exe.new")
	staleFile(t, dest)
	if err := d.Fetch(context.Background(), "0.13.0", dest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "new binary" {
		t.Fatalf("%s holds %q, err %v", dest, got, err)
	}
	only(t, filepath.Dir(dest), "nearhost.exe.new")
}

// A file at the download's path from an earlier update is gone when this
// one fails, so nothing can rename it into place.
func TestFetchFailureLeavesNoStaleFile(t *testing.T) {
	const version = "0.13.0"
	for name, d := range map[string]Downloader{
		"checksum":  release(t, version, zipOf(t, map[string]string{"nearhost.exe": "tampered"}), strings.Repeat("0", 64)+"  nearhost_"+version+"_windows_amd64.zip\n"),
		"missing":   release(t, "0.12.0", nil, ""),
		"no binary": release(t, version, zipOf(t, map[string]string{"README.md": "x"}), ""),
	} {
		dest := filepath.Join(folder(t), "nearhost.exe.new")
		staleFile(t, dest)
		if err := d.Fetch(context.Background(), version, dest); err == nil {
			t.Fatalf("%s: the download succeeded", name)
		}
		only(t, filepath.Dir(dest))
	}
}

// Cancelling partway through the archive leaves nothing behind.
func TestFetchCancelledMidwayLeavesNothing(t *testing.T) {
	const version = "0.13.0"
	archive := zipOf(t, map[string]string{"nearhost.exe": strings.Repeat("x", 1<<16)})
	sum := sha256.Sum256(archive)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprintf(w, "%s  nearhost_%s_windows_amd64.zip\n", hex.EncodeToString(sum[:]), version)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.Write(archive[:len(archive)/2])
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(folder(t), "nearhost.exe.new")
	staleFile(t, dest)
	err := Downloader{BaseURL: srv.URL, GOARCH: "amd64"}.Fetch(ctx, version, dest)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want it cancelled", err)
	}
	only(t, filepath.Dir(dest))
}

// MoveIntoPlace tries again while another program, such as a virus scanner
// reading the new file, holds it open.
func TestRetryRenameWaitsOutABusyFile(t *testing.T) {
	busy := errors.New("busy")
	tries, sleeps := 0, 0
	rename := retryRename(func(string, string) error {
		tries++
		if tries < 3 {
			return busy
		}
		return nil
	}, func(err error) bool { return err == busy }, func(time.Duration) { sleeps++ })
	if err := rename("a", "b"); err != nil || tries != 3 || sleeps != 2 {
		t.Fatalf("err %v after %d tries and %d sleeps", err, tries, sleeps)
	}

	tries = 0
	other := errors.New("other")
	rename = retryRename(func(string, string) error { tries++; return other },
		func(err error) bool { return err == busy }, func(time.Duration) { t.Fatal("slept on an error that does not pass") })
	if err := rename("a", "b"); err != other || tries != 1 {
		t.Fatalf("err %v after %d tries", err, tries)
	}

	var slept []time.Duration
	rename = retryRename(func(string, string) error { return busy },
		func(err error) bool { return err == busy }, func(d time.Duration) { slept = append(slept, d) })
	if err := rename("a", "b"); !errors.Is(err, busy) {
		t.Fatalf("err %v, want the busy error at the end", err)
	}
	// The update waits 100 ms at a time, for up to 2 s.
	if len(slept) != 20 || slept[0] != 100*time.Millisecond {
		t.Fatalf("slept %v, want 20 waits of 100ms", slept)
	}
}

// A nearhost.exe that unpacks to more than maxExe is refused, and nothing
// is left behind.
func TestFetchRefusesAnOversizedExe(t *testing.T) {
	saved := maxExe
	t.Cleanup(func() { maxExe = saved })
	maxExe = 16
	d := release(t, "0.13.0", zipOf(t, map[string]string{"nearhost.exe": strings.Repeat("x", 17)}), "")
	dest := filepath.Join(folder(t), "nearhost.exe.new")
	err := d.Fetch(context.Background(), "0.13.0", dest)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err %v, want it refused as too large", err)
	}
	only(t, filepath.Dir(dest))
	maxExe = 17
	if err := d.Fetch(context.Background(), "0.13.0", dest); err != nil {
		t.Fatalf("an exe of exactly maxExe was refused: %v", err)
	}
}
