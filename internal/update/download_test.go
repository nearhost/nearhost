package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := d.Install(context.Background(), "v0.13.0-rc.2", exe); err != nil {
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
	err := d.Install(context.Background(), version, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err %v, want a checksum error", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWithoutAChecksumLine(t *testing.T) {
	const version = "0.13.0"
	d := release(t, version, zipOf(t, map[string]string{"nearhost.exe": "x"}), "aaaa  nearhost_0.13.0_linux_amd64.tar.gz\n")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Install(context.Background(), version, exe)
	if err == nil || !strings.Contains(err.Error(), "nearhost_0.13.0_windows_amd64.zip") {
		t.Fatalf("err %v, want it to name the missing archive", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWhenTheReleaseIsMissing(t *testing.T) {
	d := release(t, "0.13.0", nil, "")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Install(context.Background(), "0.14.0", exe)
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "/v0.14.0/checksums.txt") {
		t.Fatalf("err %v, want the address and the status", err)
	}
	only(t, filepath.Dir(exe))
}

func TestDownloadFailsWhenTheArchiveHasNoBinary(t *testing.T) {
	d := release(t, "0.13.0", zipOf(t, map[string]string{"README.md": "x"}), "")
	exe := filepath.Join(folder(t), "nearhost.exe")
	err := d.Install(context.Background(), "0.13.0", exe)
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
	if err := d.Install(ctx, "0.13.0", exe); err == nil {
		t.Fatal("a cancelled download succeeded")
	}
	only(t, filepath.Dir(exe))
}
