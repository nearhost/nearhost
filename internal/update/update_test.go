package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
