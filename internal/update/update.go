// Package update finds out whether a newer nearhost has been released, and
// how to install it the way this copy was installed.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// ReleasesURL is GitHub's API for the latest published release.
const ReleasesURL = "https://api.github.com/repos/nearhost/nearhost/releases/latest"

// ReleasesListURL is GitHub's API for recent releases of every kind, which a
// prerelease build reads to find the highest version.
const ReleasesListURL = "https://api.github.com/repos/nearhost/nearhost/releases?per_page=30"

// TTL is how long a check is remembered before nearhost asks GitHub again.
const TTL = 24 * time.Hour

// Newer reports whether latest is a later release than current. A current
// that is not a version, as in a `dev` build, is never out of date. A
// pre-release latest is offered only when current is itself a pre-release,
// so a normal build never leaves the normal releases.
func Newer(current, latest string) bool {
	c, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	l, err := semver.NewVersion(latest)
	if err != nil || (l.Prerelease() != "" && c.Prerelease() == "") {
		return false
	}
	return l.GreaterThan(c)
}

// IsPrerelease reports whether v is a version with a pre-release part, such
// as 0.13.0-rc.1.
func IsPrerelease(v string) bool {
	sv, err := semver.NewVersion(v)
	return err == nil && sv.Prerelease() != ""
}

// Checker asks GitHub for the latest release, remembering the answer in a
// cache file.
type Checker struct {
	// HTTP is the client; nil means one with a short timeout.
	HTTP *http.Client
	// Current is the running version. When it is a pre-release, Latest is
	// the highest version of any kind, not only GitHub's latest release.
	Current string
	// URL is the releases endpoint; empty means ReleasesURL, or
	// ReleasesListURL when Current is a pre-release.
	URL string
	// CachePath is where the last check is kept; empty means no cache.
	CachePath string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// url is the endpoint to ask.
func (c Checker) url() string {
	switch {
	case c.URL != "":
		return c.URL
	case IsPrerelease(strings.TrimPrefix(c.Current, "v")):
		return ReleasesListURL
	}
	return ReleasesURL
}

// line names the line of releases the checker follows, kept in the cache so
// one line's answer is never read as the other's.
func (c Checker) line() string {
	if IsPrerelease(strings.TrimPrefix(c.Current, "v")) {
		return "pre"
	}
	return ""
}

// Latest asks GitHub for the latest release and returns its version without
// the leading v. On the pre-release line it returns the highest version among
// the published releases, drafts excluded.
func (c Checker) Latest(ctx context.Context) (string, error) {
	url := c.url()
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
	if c.line() == "pre" {
		return highest(resp.Body)
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

// highest reads a list of releases and returns the greatest version among
// those that are not drafts, without the leading v.
func highest(body io.Reader) (string, error) {
	var rels []struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(body).Decode(&rels); err != nil {
		return "", fmt.Errorf("could not read GitHub's list of releases: %w", err)
	}
	var best *semver.Version
	for _, r := range rels {
		v, err := semver.NewVersion(r.TagName)
		if r.Draft || err != nil {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}
	if best == nil {
		return "", errors.New("GitHub's list of releases has no nearhost version")
	}
	return best.String(), nil
}

// cache is the cache file's content. A failed check is kept too, with no
// version, so a machine offline asks once a day rather than on every run.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	// Line is "pre" for an answer found on the pre-release line.
	Line string `json:"line,omitempty"`
}

// fresh reads the cache and reports it, with whether it is less than TTL old.
func (c Checker) fresh() (cache, bool) {
	if c.CachePath == "" {
		return cache{}, false
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	data, err := os.ReadFile(c.CachePath)
	if err != nil {
		return cache{}, false
	}
	var kept cache
	if json.Unmarshal(data, &kept) != nil || now().Sub(kept.CheckedAt) >= TTL || now().Before(kept.CheckedAt) || kept.Line != c.line() {
		return cache{}, false
	}
	return kept, true
}

// Cached is Latest, answered from the cache when it is less than TTL old.
func (c Checker) Cached(ctx context.Context) (string, error) {
	if kept, ok := c.fresh(); ok {
		if kept.Latest == "" {
			return "", errors.New("the last check for a newer nearhost failed")
		}
		return kept.Latest, nil
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	latest, err := c.Latest(ctx)
	if c.CachePath != "" && ctx.Err() == nil {
		c.save(cache{CheckedAt: now(), Latest: latest, Line: c.line()})
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
// absolute, otherwise %LOCALAPPDATA%\nearhost\cache on Windows or ~/.cache
// elsewhere.
func CachePath() (string, error) { return cachePathFor(runtime.GOOS) }

// cachePathFor is CachePath on the operating system goos.
func cachePathFor(goos string) (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost", "update.json"), nil
	}
	if goos == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(local) {
			return "", errors.New("%LOCALAPPDATA% is not set to an absolute path")
		}
		return filepath.Join(local, "nearhost", "cache", "update.json"), nil
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
