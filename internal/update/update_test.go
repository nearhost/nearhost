package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

const releaseList = `[
 {"tag_name":"v0.14.0","draft":true,"prerelease":false},
 {"tag_name":"v0.13.0-rc.2","draft":false,"prerelease":true},
 {"tag_name":"nightly","draft":false,"prerelease":false},
 {"tag_name":"2024.1","draft":false,"prerelease":false},
 {"tag_name":"v0.13.0-rc.1","draft":false,"prerelease":true},
 {"tag_name":"v0.12.0","draft":false,"prerelease":false}]`

func TestIsPrerelease(t *testing.T) {
	for v, want := range map[string]bool{"0.13.0-rc.1": true, "v0.13.0-rc.1": true, "0.13.0": false, "dev": false, "": false} {
		if got := IsPrerelease(v); got != want {
			t.Errorf("IsPrerelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLatestOnThePrereleaseLineReadsTheList(t *testing.T) {
	var hits atomic.Int32
	c := Checker{Current: "0.13.0-rc.1", URL: listServer(t, releaseList, &hits, nil).URL}
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
	pre := Checker{Current: "0.13.0-rc.1", URL: listServer(t, releaseList, &hits, nil).URL, CachePath: path}
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
