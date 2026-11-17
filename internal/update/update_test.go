package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"0.5.0", "0.6.0", true},
		{"0.5.0", "v0.6.0", true},
		{"v0.5.0", "0.5.0", false},
		{"0.6.0", "0.5.0", false},
		{"0.5.0", "0.5.1", true},
		{"0.6.0-rc.1", "0.6.0", true},
		{"0.5.0", "0.6.0-rc.1", false},
		{"dev", "0.6.0", false},
		{"0.5.0", "garbage", false},
		{"", "0.6.0", false},
		{"0.12.0", "0.13.0-rc.1", false},
		{"0.13.0-rc.1", "0.13.0-rc.2", true},
		{"0.13.0-rc.2", "0.13.0", true},
		{"0.13.0-rc.2", "0.12.5", false},
		{"0.13.0-rc.1", "0.13.0-rc.1", false},
	} {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func releaseServer(t *testing.T, tag string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if tag == "" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"x"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatest(t *testing.T) {
	var hits atomic.Int32
	c := Checker{URL: releaseServer(t, "v0.6.0", &hits).URL}
	got, err := c.Latest(context.Background())
	if err != nil || got != "0.6.0" {
		t.Fatalf("Latest = %q, %v; want 0.6.0", got, err)
	}
}

func TestLatestError(t *testing.T) {
	var hits atomic.Int32
	c := Checker{URL: releaseServer(t, "", &hits).URL}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("Latest succeeded on a 500")
	}
}

func TestCachedUsesFreshCache(t *testing.T) {
	var hits atomic.Int32
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := Checker{
		URL:       releaseServer(t, "v0.6.0", &hits).URL,
		CachePath: filepath.Join(t.TempDir(), "nearhost", "update.json"),
		Now:       func() time.Time { return now },
	}
	for range 2 {
		got, err := c.Cached(context.Background())
		if err != nil || got != "0.6.0" {
			t.Fatalf("Cached = %q, %v", got, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times, want 1", hits.Load())
	}
	now = now.Add(25 * time.Hour)
	if _, err := c.Cached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("stale cache fetched %d times in all, want 2", hits.Load())
	}
}

func TestCachedFailureIsRememberedToo(t *testing.T) {
	var hits atomic.Int32
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := Checker{
		URL:       releaseServer(t, "", &hits).URL,
		CachePath: filepath.Join(t.TempDir(), "update.json"),
		Now:       func() time.Time { return now },
	}
	for range 2 {
		if _, err := c.Cached(context.Background()); err == nil {
			t.Fatal("Cached succeeded on a 500")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("an offline machine fetched %d times in a day, want 1", hits.Load())
	}
}

func TestCachedIgnoresCorruptCache(t *testing.T) {
	var hits atomic.Int32
	path := filepath.Join(t.TempDir(), "update.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	c := Checker{URL: releaseServer(t, "v0.6.0", &hits).URL, CachePath: path}
	got, err := c.Cached(context.Background())
	if err != nil || got != "0.6.0" {
		t.Fatalf("Cached = %q, %v", got, err)
	}
}

func TestCachePathForEachOS(t *testing.T) {
	home, localAppData, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	// os.UserHomeDir reads USERPROFILE on Windows.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("XDG_CACHE_HOME", "")
	for goos, want := range map[string]string{
		"darwin":  filepath.Join(home, ".cache", "nearhost", "update.json"),
		"linux":   filepath.Join(home, ".cache", "nearhost", "update.json"),
		"windows": filepath.Join(localAppData, "nearhost", "cache", "update.json"),
	} {
		if got, err := cachePathFor(goos); err != nil || got != want {
			t.Errorf("cachePathFor(%s): got %q, %v; want %q", goos, got, err, want)
		}
	}
	t.Setenv("XDG_CACHE_HOME", xdg)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if got, err := cachePathFor(goos); err != nil || got != filepath.Join(xdg, "nearhost", "update.json") {
			t.Errorf("cachePathFor(%s) with XDG_CACHE_HOME: got %q, %v", goos, got, err)
		}
	}
	t.Setenv("XDG_CACHE_HOME", "")
	for _, local := range []string{"", "relative"} {
		t.Setenv("LOCALAPPDATA", local)
		if _, err := cachePathFor("windows"); err == nil || !strings.Contains(err.Error(), "%LOCALAPPDATA%") {
			t.Errorf("cachePathFor(windows) with LOCALAPPDATA %q: %v, want an error naming %%LOCALAPPDATA%%", local, err)
		}
	}
}

func listServer(t *testing.T, body string, hits *atomic.Int32, paths *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if paths != nil {
			*paths = append(*paths, r.URL.Path)
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// releaseList is GitHub's list of releases, each with a linux/amd64 build;
// tests on the pre-release line set GOOS and GOARCH to match.
const releaseList = `[
 {"tag_name":"v0.14.0","draft":true,"prerelease":false,"assets":[{"name":"nearhost_0.14.0_linux_amd64.tar.gz"}]},
 {"tag_name":"v0.13.0-rc.2","draft":false,"prerelease":true,"assets":[{"name":"nearhost_0.13.0-rc.2_linux_amd64.tar.gz"}]},
 {"tag_name":"nightly","draft":false,"prerelease":false,"assets":[{"name":"nearhost_nightly_linux_amd64.tar.gz"}]},
 {"tag_name":"2024.1","draft":false,"prerelease":false,"assets":[{"name":"nearhost_2024.1.0_linux_amd64.tar.gz"}]},
 {"tag_name":"v0.13.0-rc.1","draft":false,"prerelease":true,"assets":[{"name":"nearhost_0.13.0-rc.1_linux_amd64.tar.gz"}]},
 {"tag_name":"v0.12.0","draft":false,"prerelease":false,"assets":[{"name":"nearhost_0.12.0_linux_amd64.tar.gz"}]}]`

// A pre-release build is offered only a release with a build for its
// platform: while Windows ships only in pre-releases, a later normal release
// has no zip, and a release whose assets are not uploaded yet has none.
func TestLatestOnThePrereleaseLineNeedsABuildForThisPlatform(t *testing.T) {
	const list = `[
 {"tag_name":"v0.14.0","draft":false,"prerelease":false,"assets":[]},
 {"tag_name":"v0.13.1","draft":false,"prerelease":false},
 {"tag_name":"v0.13.0","draft":false,"prerelease":false,"assets":[
  {"name":"checksums.txt"},
  {"name":"nearhost_0.13.0_darwin_arm64.tar.gz"},
  {"name":"nearhost_0.13.0_linux_amd64.tar.gz"},
  {"name":"nearhost_0.13.0_linux_arm64.tar.gz"}]},
 {"tag_name":"v0.13.0-rc.2","draft":false,"prerelease":true,"assets":[
  {"name":"checksums.txt"},
  {"name":"nearhost_0.13.0-rc.2_linux_amd64.tar.gz"},
  {"name":"nearhost_0.13.0-rc.2_linux_arm64.tar.gz"},
  {"name":"nearhost_0.13.0-rc.2_windows_amd64.zip"},
  {"name":"nearhost_0.13.0-rc.2_windows_arm64.zip"}]},
 {"tag_name":"v0.13.0-rc.1","draft":false,"prerelease":true,"assets":[
  {"name":"nearhost_0.13.0-rc.1_windows_amd64.zip"}]}]`
	for _, tc := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", "0.13.0-rc.2"},
		{"windows", "arm64", "0.13.0-rc.2"},
		{"linux", "amd64", "0.13.0"},
		{"darwin", "arm64", "0.13.0"},
		{"darwin", "amd64", ""},
	} {
		var hits atomic.Int32
		c := Checker{Current: "0.13.0-rc.1", URL: listServer(t, list, &hits, nil).URL, GOOS: tc.goos, GOARCH: tc.goarch}
		got, err := c.Latest(context.Background())
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s/%s: Latest = %q, want an error, as no release has its build", tc.goos, tc.goarch, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s/%s: Latest = %q, %v; want %s", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
}

func TestAssetNames(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", "nearhost_0.13.0-rc.1_windows_amd64.zip"},
		{"windows", "arm64", "nearhost_0.13.0-rc.1_windows_arm64.zip"},
		{"linux", "arm64", "nearhost_0.13.0-rc.1_linux_arm64.tar.gz"},
		{"darwin", "amd64", "nearhost_0.13.0-rc.1_darwin_amd64.tar.gz"},
	} {
		if got := (Checker{GOOS: tc.goos, GOARCH: tc.goarch}).asset("0.13.0-rc.1"); got != tc.want {
			t.Errorf("asset = %q, want %q", got, tc.want)
		}
	}
	if got, want := (Checker{}).asset("1.0.0"), "nearhost_1.0.0_"+runtime.GOOS+"_"+runtime.GOARCH; !strings.HasPrefix(got, want) {
		t.Errorf("default asset = %q, want the running platform's", got)
	}
}

func TestIsPrerelease(t *testing.T) {
	for v, want := range map[string]bool{"0.13.0-rc.1": true, "v0.13.0-rc.1": true, "0.13.0": false, "dev": false, "": false} {
		if got := IsPrerelease(v); got != want {
			t.Errorf("IsPrerelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLatestOnThePrereleaseLineReadsTheList(t *testing.T) {
	var hits atomic.Int32
	c := Checker{Current: "0.13.0-rc.1", URL: listServer(t, releaseList, &hits, nil).URL, GOOS: "linux", GOARCH: "amd64"}
	got, err := c.Latest(context.Background())
	if err != nil || got != "0.13.0-rc.2" {
		t.Fatalf("Latest = %q, %v; want 0.13.0-rc.2 (not the draft, nor the loose tag 2024.1)", got, err)
	}
}

func TestLatestForANormalBuildNeverReadsTheList(t *testing.T) {
	var hits atomic.Int32
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		paths = append(paths, r.URL.Path)
		w.Write([]byte(`{"tag_name":"v0.12.0"}`))
	}))
	defer srv.Close()
	// The default URL is GitHub's; here the test points URL at a server and
	// checks the request is for a single release, not a list.
	c := Checker{Current: "0.12.0", URL: srv.URL + "/releases/latest"}
	got, err := c.Latest(context.Background())
	if err != nil || got != "0.12.0" || hits.Load() != 1 || paths[0] != "/releases/latest" {
		t.Fatalf("Latest = %q, %v, paths %v", got, err, paths)
	}
}

func TestDefaultURLs(t *testing.T) {
	if ReleasesListURL != "https://api.github.com/repos/nearhost/nearhost/releases?per_page=30" {
		t.Errorf("ReleasesListURL = %s", ReleasesListURL)
	}
	if (Checker{}).url() != ReleasesURL || (Checker{Current: "1.0.0"}).url() != ReleasesURL || (Checker{Current: "1.0.0-rc.1"}).url() != ReleasesListURL {
		t.Error("default URL does not follow the build's line")
	}
}

func TestLatestListWithNothingUsable(t *testing.T) {
	var hits atomic.Int32
	c := Checker{Current: "0.13.0-rc.1", URL: listServer(t, `[{"tag_name":"v1.0.0","draft":true}]`, &hits, nil).URL}
	if got, err := c.Latest(context.Background()); err == nil {
		t.Fatalf("Latest = %q, want an error", got)
	}
}

func TestCachedPrereleaseAnswerIsNeverShownToANormalBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	var hits atomic.Int32
	pre := Checker{Current: "0.13.0-rc.1", URL: listServer(t, releaseList, &hits, nil).URL, CachePath: path, GOOS: "linux", GOARCH: "amd64"}
	if got, err := pre.Cached(context.Background()); err != nil || got != "0.13.0-rc.2" {
		t.Fatalf("Cached = %q, %v", got, err)
	}
	var normalHits atomic.Int32
	normal := Checker{Current: "0.12.0", URL: releaseServer(t, "v0.12.0", &normalHits).URL, CachePath: path}
	got, err := normal.Cached(context.Background())
	if err != nil || got != "0.12.0" || normalHits.Load() != 1 {
		t.Fatalf("normal build got %q, %v (asked GitHub %d times); the prerelease line's cache must not serve it", got, err, normalHits.Load())
	}
	if Newer("0.12.0", "0.13.0-rc.2") {
		t.Error("a normal build would be offered the cached prerelease")
	}
}
