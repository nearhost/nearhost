package update

import (
	"context"
	"encoding/json"
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
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"x","assets":[{"name":"nearhost_` + strings.TrimPrefix(tag, "v") + "_" + runtime.GOOS + "_" + runtime.GOARCH + archiveExt() + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// archiveExt is the running platform's archive extension.
func archiveExt() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// assetServer serves one latest release with the given asset names.
func assetServer(t *testing.T, tag string, names ...string) *httptest.Server {
	t.Helper()
	assets := make([]string, len(names))
	for i, n := range names {
		assets[i] = `{"name":"` + n + `"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `","assets":[` + strings.Join(assets, ",") + `]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A release whose archive for this platform is not uploaded yet is not out:
// no update, no error.
func TestLatestWithoutThisPlatformsArchiveIsNotOut(t *testing.T) {
	for name, names := range map[string][]string{
		"no assets":       nil,
		"checksums only":  {"checksums.txt"},
		"other platform":  {"checksums.txt", "nearhost_0.6.0_darwin_arm64.tar.gz", "nearhost_0.6.0_linux_arm64.tar.gz", "nearhost_0.6.0_windows_amd64.zip"},
		"wrong extension": {"nearhost_0.6.0_linux_amd64.zip"},
	} {
		c := Checker{URL: assetServer(t, "v0.6.0", names...).URL, GOOS: "linux", GOARCH: "amd64"}
		got, err := c.Latest(context.Background())
		if err != nil || got != "" {
			t.Errorf("%s: Latest = %q, %v; want no update and no error", name, got, err)
		}
	}
}

func TestLatestWithThisPlatformsArchiveIsOffered(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, name string }{
		{"linux", "amd64", "nearhost_0.6.0_linux_amd64.tar.gz"},
		{"linux", "arm64", "nearhost_0.6.0_linux_arm64.tar.gz"},
		{"darwin", "arm64", "nearhost_0.6.0_darwin_arm64.tar.gz"},
		{"darwin", "amd64", "nearhost_0.6.0_darwin_amd64.tar.gz"},
		{"windows", "amd64", "nearhost_0.6.0_windows_amd64.zip"},
		{"windows", "arm64", "nearhost_0.6.0_windows_arm64.zip"},
	} {
		c := Checker{URL: assetServer(t, "v0.6.0", "checksums.txt", tc.name).URL, GOOS: tc.goos, GOARCH: tc.goarch}
		got, err := c.Latest(context.Background())
		if err != nil || got != "0.6.0" {
			t.Errorf("%s/%s: Latest = %q, %v; want 0.6.0", tc.goos, tc.goarch, got, err)
		}
	}
}

// A skipped release is remembered for 15 minutes, so the notice appears soon
// after the files land; a normal answer keeps its 24 hours.
func TestCachedSkippedReleaseIsRememberedFor15Minutes(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	var hits atomic.Int32
	var assets atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if assets.Load() {
			w.Write([]byte(`{"tag_name":"v0.6.0","assets":[{"name":"nearhost_0.6.0_linux_amd64.tar.gz"}]}`))
			return
		}
		w.Write([]byte(`{"tag_name":"v0.6.0","assets":[]}`))
	}))
	defer srv.Close()
	c := Checker{URL: srv.URL, CachePath: path, Now: func() time.Time { return now }, GOOS: "linux", GOARCH: "amd64"}
	if got, err := c.Cached(context.Background()); err != nil || got != "" {
		t.Fatalf("Cached = %q, %v", got, err)
	}
	now = now.Add(14 * time.Minute)
	if got, err := c.Cached(context.Background()); err != nil || got != "" || hits.Load() != 1 {
		t.Fatalf("after 14 min: %q, %v, %d fetches; want the cached no-update", got, err, hits.Load())
	}
	assets.Store(true)
	now = now.Add(2 * time.Minute)
	if got, err := c.Cached(context.Background()); err != nil || got != "0.6.0" || hits.Load() != 2 {
		t.Fatalf("after 16 min: %q, %v, %d fetches; want 0.6.0 from a second fetch", got, err, hits.Load())
	}
	// The normal answer now stays for 24 hours.
	now = now.Add(23 * time.Hour)
	if got, err := c.Cached(context.Background()); err != nil || got != "0.6.0" || hits.Load() != 2 {
		t.Fatalf("after 23 h: %q, %v, %d fetches; want the cached 0.6.0", got, err, hits.Load())
	}
}

// An older build reads a newer build's skipped entry as a failed check, and
// the entry is dated so that the older build's 24 hours end after 15 minutes.
func TestSkippedCacheEntryExpiresForAnOlderBuildToo(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	c := Checker{URL: assetServer(t, "v0.6.0").URL, CachePath: path, Now: func() time.Time { return now }, GOOS: "linux", GOARCH: "amd64"}
	if _, err := c.Cached(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// What the previous build's struct sees.
	var old struct {
		CheckedAt time.Time `json:"checked_at"`
		Latest    string    `json:"latest,omitempty"`
		Line      string    `json:"line,omitempty"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("an older build cannot read the entry: %v", err)
	}
	if old.Latest != "" {
		t.Errorf("older build sees latest %q", old.Latest)
	}
	if age := now.Add(16 * time.Minute).Sub(old.CheckedAt); age < TTL {
		t.Errorf("an older build would trust the entry for %v, want it stale after 15 minutes", TTL-age)
	}
	if age := now.Add(10 * time.Minute).Sub(old.CheckedAt); age >= TTL {
		t.Errorf("entry already stale after 10 minutes")
	}
}

// A cache file an older build wrote has no skipped marker and reads as before.
func TestCachedReadsAnOlderBuildsFile(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	old := `{"checked_at":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","latest":"0.5.0"}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Checker{URL: "http://127.0.0.1:1/unreachable", CachePath: path, Now: func() time.Time { return now }}
	if got, err := c.Cached(context.Background()); err != nil || got != "0.5.0" {
		t.Fatalf("Cached = %q, %v; want 0.5.0 from the older build's file", got, err)
	}
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
		w.Write([]byte(`{"tag_name":"v0.12.0","assets":[{"name":"nearhost_0.12.0_linux_amd64.tar.gz"}]}`))
	}))
	defer srv.Close()
	// The default URL is GitHub's; here the test points URL at a server and
	// checks the request is for a single release, not a list.
	c := Checker{Current: "0.12.0", URL: srv.URL + "/releases/latest", GOOS: "linux", GOARCH: "amd64"}
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

// A release build asked for the beta line reads the list of releases, as a
// pre-release build does.
func TestLatestWithNextReadsTheList(t *testing.T) {
	var hits atomic.Int32
	c := Checker{Current: "0.12.0", Next: true, URL: listServer(t, releaseList, &hits, nil).URL, GOOS: "linux", GOARCH: "amd64"}
	got, err := c.Latest(context.Background())
	if err != nil || got != "0.13.0-rc.2" {
		t.Fatalf("Latest = %q, %v; want 0.13.0-rc.2", got, err)
	}
	if (Checker{Current: "1.0.0", Next: true}).url() != ReleasesListURL {
		t.Error("a Next check does not default to the list of releases")
	}
}

func TestNextAnswerIsNeverShownToANormalBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	var hits atomic.Int32
	next := Checker{Current: "0.12.0", Next: true, URL: listServer(t, releaseList, &hits, nil).URL, CachePath: path, GOOS: "linux", GOARCH: "amd64"}
	if got, err := next.Cached(context.Background()); err != nil || got != "0.13.0-rc.2" {
		t.Fatalf("Cached = %q, %v", got, err)
	}
	var normalHits atomic.Int32
	normal := Checker{Current: "0.12.0", URL: releaseServer(t, "v0.12.0", &normalHits).URL, CachePath: path}
	got, err := normal.Cached(context.Background())
	if err != nil || got != "0.12.0" || normalHits.Load() != 1 {
		t.Fatalf("normal build got %q, %v (asked GitHub %d times); the beta line's cache must not serve it", got, err, normalHits.Load())
	}
}

func TestNewerNext(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"0.15.0", "0.16.0-beta.1", true},
		{"0.16.0-beta.1", "0.16.0-beta.2", true},
		{"0.16.0-beta.10", "0.16.0-beta.9", false},
		{"0.16.0-beta.9", "0.16.0-beta.10", true},
		{"0.16.0", "0.16.0-beta.3", false},
		{"0.16.0", "0.16.0", false},
		{"0.15.0", "0.16.0", true},
		{"dev", "0.16.0", false},
		{"0.15.0", "", false},
	} {
		if got := NewerNext(tc.current, tc.latest); got != tc.want {
			t.Errorf("NewerNext(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}
