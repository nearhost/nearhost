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
	"slices"
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

// SkippedTTL is how long a check is remembered when the newest release has
// no archive for this platform yet, so the notice appears soon after the
// release pipeline uploads the files.
const SkippedTTL = 15 * time.Minute

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

// NewerNext reports whether latest is a later version than current, of any
// kind: unlike Newer, a release build may be offered a pre-release, as
// `nearhost update --next` asks for. A current or latest that is not a
// version is never newer.
func NewerNext(current, latest string) bool {
	c, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	l, err := semver.NewVersion(latest)
	return err == nil && l.GreaterThan(c)
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
	// Next asks for the beta line whatever Current is, as `nearhost update
	// --next` does: Latest is then the highest version of any kind, as for a
	// pre-release build, and is cached on that line.
	Next bool
	// URL is the releases endpoint; empty means ReleasesURL, or
	// ReleasesListURL when Current is a pre-release or Next is set.
	URL string
	// CachePath is where the last check is kept; empty means no cache.
	CachePath string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// GOOS and GOARCH are the platform a pre-release build looks for a
	// build of; empty means the running one's.
	GOOS, GOARCH string
}

// asset is the name of this platform's archive in the release of version, as
// .goreleaser.yaml names it: a zip on Windows, a tar.gz elsewhere.
func (c Checker) asset(version string) string {
	goos, goarch := c.GOOS, c.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "nearhost_" + version + "_" + goos + "_" + goarch + ext
}

// url is the endpoint to ask.
func (c Checker) url() string {
	switch {
	case c.URL != "":
		return c.URL
	case c.Next || IsPrerelease(strings.TrimPrefix(c.Current, "v")):
		return ReleasesListURL
	}
	return ReleasesURL
}

// line names the line of releases the checker follows, kept in the cache so
// one line's answer is never read as the other's.
func (c Checker) line() string {
	if c.Next || IsPrerelease(strings.TrimPrefix(c.Current, "v")) {
		return "pre"
	}
	return ""
}

// Latest asks GitHub for the latest release and returns its version without
// the leading v. On the pre-release line it returns the highest version among
// the published releases that have a build for this platform, drafts
// excluded. On the normal line the release must list this platform's archive:
// a release whose files are not uploaded yet counts as not out, and Latest
// returns an empty version and no error, which Newer reads as no update. It
// does not fall back to the previous release, as the latest-release endpoint
// returns only the newest one.
func (c Checker) Latest(ctx context.Context) (string, error) {
	v, _, err := c.latest(ctx)
	return v, err
}

// latest is Latest, also reporting whether the newest release was skipped
// because it lists no archive for this platform.
func (c Checker) latest(ctx context.Context) (version string, skipped bool, err error) {
	url := c.url()
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("could not reach GitHub to check for a newer nearhost: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("GitHub answered %s when asked for the latest nearhost release", resp.Status)
	}
	if c.line() == "pre" {
		v, err := c.highest(resp.Body)
		return v, false, err
	}
	var rel struct {
		TagName string         `json:"tag_name"`
		Assets  []releaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", false, fmt.Errorf("could not read GitHub's latest release: %w", err)
	}
	if rel.TagName == "" {
		return "", false, errors.New("GitHub's latest release has no tag")
	}
	version = strings.TrimPrefix(rel.TagName, "v")
	// The same archive name highest() requires of a pre-release.
	want := c.asset(version)
	if !slices.ContainsFunc(rel.Assets, func(a releaseAsset) bool { return a.Name == want }) {
		return "", true, nil
	}
	return version, false, nil
}

// highest reads a list of releases and returns the greatest version among
// those that are not drafts and carry this platform's archive, without the
// leading v. A release without it has no build here, such as a normal
// release while Windows ships only in pre-releases, or one whose assets are
// not uploaded yet.
func (c Checker) highest(body io.Reader) (string, error) {
	var rels []struct {
		TagName string         `json:"tag_name"`
		Draft   bool           `json:"draft"`
		Assets  []releaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(body).Decode(&rels); err != nil {
		return "", fmt.Errorf("could not read GitHub's list of releases: %w", err)
	}
	var best *semver.Version
	for _, r := range rels {
		// Strictly, so a loose tag such as 2024.1 cannot win.
		v, err := semver.StrictNewVersion(strings.TrimPrefix(r.TagName, "v"))
		if r.Draft || err != nil {
			continue
		}
		want := c.asset(v.String())
		if !slices.ContainsFunc(r.Assets, func(a releaseAsset) bool { return a.Name == want }) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}
	if best == nil {
		return "", errors.New("GitHub's list of releases has no nearhost version with a build for this platform")
	}
	return best.String(), nil
}

// releaseAsset is a file attached to a release.
type releaseAsset struct {
	Name string `json:"name"`
}

// cache is the cache file's content. A failed check is kept too, with no
// version, so a machine offline asks once a day rather than on every run.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	// Line is "pre" for an answer found on the pre-release line.
	Line string `json:"line,omitempty"`
	// Skipped marks a check whose newest release had no archive for this
	// platform yet. It has no Latest and is dated so that the entry ends
	// SkippedTTL after the check: a build that does not know the field reads
	// it as a failed check and, with CheckedAt back-dated, asks again after
	// the same 15 minutes.
	Skipped bool `json:"skipped,omitempty"`
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
		if kept.Skipped {
			return "", nil
		}
		if kept.Latest == "" {
			return "", errors.New("the last check for a newer nearhost failed")
		}
		return kept.Latest, nil
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	latest, skipped, err := c.latest(ctx)
	if c.CachePath != "" && ctx.Err() == nil {
		kept := cache{CheckedAt: now(), Latest: latest, Line: c.line()}
		if skipped {
			kept.Skipped = true
			kept.CheckedAt = kept.CheckedAt.Add(SkippedTTL - TTL)
		}
		c.save(kept)
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
