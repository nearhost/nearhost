package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// DownloadURL is where a release's files are, under /v<version>/.
const DownloadURL = "https://github.com/nearhost/nearhost/releases/download"

// The most Fetch reads of checksums.txt and of the archive. A release's
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
	// to Fetch bounds the download.
	HTTP *http.Client
	// BaseURL is where releases are; empty means DownloadURL.
	BaseURL string
	// GOARCH is the build to fetch; empty means the running one's.
	GOARCH string
}

// Fetch fetches the Windows zip of the release of version, checks it
// against the release's checksums.txt, and writes its nearhost.exe to dest,
// a new file beside the running copy that MoveIntoPlace then renames over
// it. A file already at dest, left by an earlier update, is removed first
// and never used. When Fetch returns nil, dest is complete and on disk; when
// it fails, a cancelled ctx among the causes, dest is gone.
func (d Downloader) Fetch(ctx context.Context, version, dest string) error {
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s, left by an earlier update: %w", dest, err)
	}
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
			return writeExe(ctx, f, dest)
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

// writeExe writes the zip entry f to dest, a file it creates, and syncs it.
// archive/zip checks the entry's size and CRC-32 as it is read. On any
// error, or when ctx has ended by the time the file is written, dest is
// removed.
func writeExe(ctx context.Context, f *zip.File, dest string) (err error) {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	// O_EXCL: Fetch removed any file at dest, so one there now is not ours.
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(dest)
		}
	}()
	if _, err = io.Copy(out, src); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err = out.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return ctx.Err()
}

// Windows refuses to rename a file another program has open, such as a
// virus scanner reading the file Fetch has just written. A rename that
// fails so is tried again every renameRetryEvery until renameRetryFor has
// passed.
const (
	renameRetryEvery = 100 * time.Millisecond
	renameRetryFor   = 2 * time.Second
)

// MoveIntoPlace renames from to to, waiting out a file that another program
// holds open for a moment.
func MoveIntoPlace(from, to string) error {
	return retryRename(os.Rename, renameBusy, time.Sleep)(from, to)
}

// retryRename returns a rename that calls try and, while busy reports its
// error as one that passes, sleeps renameRetryEvery and calls it again, for
// up to renameRetryFor of sleeping. It returns the last error.
func retryRename(try func(oldpath, newpath string) error, busy func(error) bool, sleep func(time.Duration)) func(oldpath, newpath string) error {
	return func(oldpath, newpath string) error {
		err := try(oldpath, newpath)
		for waited := time.Duration(0); err != nil && busy(err) && waited < renameRetryFor; waited += renameRetryEvery {
			sleep(renameRetryEvery)
			err = try(oldpath, newpath)
		}
		return err
	}
}
