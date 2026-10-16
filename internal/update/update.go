// Package update finds out whether a newer nearhost has been released, and
// how to install it the way this copy was installed.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// ReleasesURL is GitHub's API for the latest published release.
const ReleasesURL = "https://api.github.com/repos/nearhost/nearhost/releases/latest"

// TTL is how long a check is remembered before nearhost asks GitHub again.
const TTL = 24 * time.Hour

// Newer reports whether latest is a later release than current. A current
// that is not a version, as in a `dev` build, is never out of date, and a
// pre-release is never offered.
func Newer(current, latest string) bool {
	c, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	l, err := semver.NewVersion(latest)
	if err != nil || l.Prerelease() != "" {
		return false
	}
	return l.GreaterThan(c)
}

// Checker asks GitHub for the latest release, remembering the answer in a
// cache file.
type Checker struct {
	// HTTP is the client; nil means one with a short timeout.
	HTTP *http.Client
	// URL is the releases endpoint; empty means ReleasesURL.
	URL string
	// CachePath is where the last check is kept; empty means no cache.
	CachePath string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Latest asks GitHub for the latest release and returns its version without
// the leading v.
func (c Checker) Latest(ctx context.Context) (string, error) {
	url := c.URL
	if url == "" {
		url = ReleasesURL
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub to check for a newer nearhost: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s when asked for the latest nearhost release", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("could not read GitHub's latest release: %w", err)
	}
	if rel.TagName == "" {
		return "", errors.New("GitHub's latest release has no tag")
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

// cache is the cache file's content. A failed check is kept too, with no
// version, so a machine offline asks once a day rather than on every run.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
}

// Cached is Latest, answered from the cache when it is less than TTL old.
func (c Checker) Cached(ctx context.Context) (string, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if c.CachePath != "" {
		if data, err := os.ReadFile(c.CachePath); err == nil {
			var kept cache
			if json.Unmarshal(data, &kept) == nil && now().Sub(kept.CheckedAt) < TTL && !now().Before(kept.CheckedAt) {
				if kept.Latest == "" {
					return "", errors.New("the last check for a newer nearhost failed")
				}
				return kept.Latest, nil
			}
		}
	}
	latest, err := c.Latest(ctx)
	if c.CachePath != "" && ctx.Err() == nil {
		c.save(cache{CheckedAt: now(), Latest: latest})
	}
	return latest, err
}

// save writes the cache, ignoring failure: the cache only saves requests.
func (c Checker) save(kept cache) {
	data, err := json.Marshal(kept)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(c.CachePath), 0o755) != nil {
		return
	}
	tmp := c.CachePath + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, c.CachePath)
	}
}

// CachePath is the default cache file: under XDG_CACHE_HOME when it is
// absolute, otherwise ~/.cache.
func CachePath() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost", "update.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "nearhost", "update.json"), nil
}

// IsRelease reports whether v is a release version rather than, say, `dev`.
func IsRelease(v string) bool {
	_, err := semver.NewVersion(v)
	return err == nil
}
