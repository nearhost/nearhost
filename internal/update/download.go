package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// DownloadURL is where a release's files are, under /v<version>/.
const DownloadURL = "https://github.com/nearhost/nearhost/releases/download"

// The most Install reads of checksums.txt and of the archive. A release's
// archive is some 25 MB.
const (
	maxChecksums = 1 << 20
	maxArchive   = 512 << 20
)

// Downloader updates a Windows copy that came from the PowerShell installer
// or a zip, by fetching the release itself. Starting PowerShell to run the
// installer again is not an option: Microsoft Defender reads a hidden
// `powershell -ExecutionPolicy Bypass -Command "irm … | iex"` as malware and
// refuses to start it.
type Downloader struct {
	// HTTP is the client; nil means http.DefaultClient. The context given
	// to Install bounds the download.
	HTTP *http.Client
	// BaseURL is where releases are; empty means DownloadURL.
	BaseURL string
	// GOARCH is the build to fetch; empty means the running one's.
	GOARCH string
}

// Install fetches the Windows zip of the release of version, checks it
// against the release's checksums.txt, and writes its nearhost.exe to exe,
// which must not exist or must be replaceable (the running copy has been
// moved aside). When it fails it leaves the folder as it found it.
func (d Downloader) Install(ctx context.Context, version, exe string) error {
	version = strings.TrimPrefix(version, "v")
	goarch := d.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	base := d.BaseURL
	if base == "" {
		base = DownloadURL
	}
	base += "/v" + version + "/"
	asset := Checker{GOOS: "windows", GOARCH: goarch}.asset(version)

	sums, err := d.get(ctx, base+"checksums.txt", maxChecksums)
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return fmt.Errorf("release %s has no %s in its checksums.txt", version, asset)
	}
	archive, err := d.get(ctx, base+asset, maxArchive)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("%s does not match its checksum in the release's checksums.txt, so it was not installed", asset)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("read %s: %w", asset, err)
	}
	for _, f := range zr.File {
		if strings.EqualFold(path.Base(f.Name), "nearhost.exe") && !f.FileInfo().IsDir() {
			return writeExe(f, exe)
		}
	}
	return fmt.Errorf("%s holds no nearhost.exe", asset)
}

// get is the body at url, at most max bytes of it.
func (d Downloader) get(ctx context.Context, url string, max int64) ([]byte, error) {
	client := d.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: the server answered %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("download %s: it is larger than %d MB", url, max>>20)
	}
	return body, nil
}

// writeExe writes the zip entry f to exe by way of a file beside it, so exe
// is never half written.
func writeExe(f *zip.File, exe string) (err error) {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".nearhost-update-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, src); err != nil {
		return err
	}
	if err = tmp.Chmod(0o755); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}
