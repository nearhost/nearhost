// Package registry fetches presets from a preset registry, a public GitHub
// repository: an index that says which presets exist, and the preset files
// it names.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	// githubRaw serves the files of public GitHub repositories.
	githubRaw = "https://raw.githubusercontent.com/"
	// IndexSchema is the newest index format this build understands.
	IndexSchema = 1

	maxAge  = 24 * time.Hour  // how long a cached index is used without asking
	timeout = 3 * time.Second // per request
	maxBody = 1 << 20         // largest index or preset accepted
)

// ErrUnavailable reports that the index could not be fetched and no copy of
// it is cached.
var ErrUnavailable = errors.New("its index could not be fetched and no copy of it is saved")

// ErrNewerIndex reports an index written in a format newer than this build
// understands.
var ErrNewerIndex = errors.New("the preset registry's index needs a newer nearhost; upgrade nearhost to use it")

// Index lists the presets the registry holds.
type Index struct {
	Schema  int     `yaml:"schema"`
	Ref     string  `yaml:"ref"` // commit the entries were built from
	Presets []Entry `yaml:"presets"`
}

// Entry is what is known of a preset before it is downloaded.
type Entry struct {
	Name         string `yaml:"name"`
	Title        string `yaml:"title"`
	Description  string `yaml:"description,omitempty"`
	PresetSchema int    `yaml:"preset_schema"`
	Detect       string `yaml:"detect"`
	SHA256       string `yaml:"sha256"`
}

// Entry finds a preset by name.
func (i *Index) Entry(name string) (Entry, bool) {
	for _, e := range i.Presets {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Sum is the hash the index records for a preset file.
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	refRe  = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	sumRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseIndex reads an index. Names and the ref become parts of URLs and
// file names, so anything but plain words is refused. Keys this build does
// not know are ignored.
func ParseIndex(data []byte) (*Index, error) {
	var idx Index
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("registry index: %w", err)
	}
	switch {
	case idx.Schema > IndexSchema:
		return nil, fmt.Errorf("registry index: schema %d: %w", idx.Schema, ErrNewerIndex)
	case idx.Schema < 1:
		return nil, errors.New("registry index: schema is missing")
	case !refRe.MatchString(idx.Ref):
		return nil, fmt.Errorf("registry index: ref %q is not a commit", idx.Ref)
	}
	seen := map[string]bool{}
	for _, e := range idx.Presets {
		if !nameRe.MatchString(e.Name) || !sumRe.MatchString(e.SHA256) || e.Detect == "" || seen[e.Name] || hasControl(e.Title) || hasControl(e.Description) {
			return nil, fmt.Errorf("registry index: the entry for %q is not valid", e.Name)
		}
		seen[e.Name] = true
	}
	return &idx, nil
}

// hasControl reports whether s holds a control character, which a title or
// description must not carry to the terminal.
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// Client talks to one registry and caches its index in Dir.
type Client struct {
	BaseURL string
	Dir     string
	HTTP    *http.Client
	Now     func() time.Time
}

// ForRepo returns a client for the registry in the public GitHub repository
// repo, written owner/name, with its index cached in dir. repo must already
// be valid.
func ForRepo(repo, dir string) *Client {
	return &Client{BaseURL: githubRaw + repo, Dir: dir, HTTP: http.DefaultClient, Now: time.Now}
}

func (c *Client) cachePath() string { return filepath.Join(c.Dir, "index.yaml") }

// Index returns the registry index: the cached copy while it is under a day
// old, else a fresh one, else the stale copy.
func (c *Client) Index(ctx context.Context) (*Index, error) {
	cached, age, cacheErr := c.cached()
	if cacheErr == nil && age >= 0 && age < maxAge {
		return cached, nil
	}
	idx, err := c.Refresh(ctx)
	if err == nil {
		return idx, nil
	}
	if cacheErr == nil {
		return cached, nil
	}
	if errors.Is(err, ErrNewerIndex) {
		// The registry was reached; this build is too old to read it.
		return nil, err
	}
	return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// Refresh fetches the index now and caches it. The index is read from HEAD,
// the repository's default branch, whatever it is called.
func (c *Client) Refresh(ctx context.Context) (*Index, error) {
	data, err := c.get(ctx, c.BaseURL+"/HEAD/index.yaml")
	if err != nil {
		return nil, err
	}
	idx, err := ParseIndex(data)
	if err != nil {
		return nil, err
	}
	// A cache that cannot be written only costs the next run a request.
	_ = c.store(data)
	return idx, nil
}

func (c *Client) cached() (*Index, time.Duration, error) {
	info, err := os.Stat(c.cachePath())
	if err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(c.cachePath())
	if err != nil {
		return nil, 0, err
	}
	idx, err := ParseIndex(data)
	if err != nil {
		return nil, 0, err
	}
	return idx, c.Now().Sub(info.ModTime()), nil
}

func (c *Client) store(data []byte) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".index-*.yaml")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.cachePath()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	now := c.Now()
	return os.Chtimes(c.cachePath(), now, now)
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, fmt.Errorf("GET %s: the response is larger than %d bytes", url, maxBody)
	}
	return data, nil
}

// Fetch downloads a preset at the index's ref and checks it against the
// hash the index records.
func (c *Client) Fetch(ctx context.Context, idx *Index, name string) ([]byte, error) {
	e, ok := idx.Entry(name)
	if !ok {
		return nil, fmt.Errorf("the registry has no preset named %q", name)
	}
	data, err := c.get(ctx, fmt.Sprintf("%s/%s/presets/%s.yaml", c.BaseURL, idx.Ref, name))
	if err != nil {
		return nil, fmt.Errorf("download preset %s: %w", name, err)
	}
	if got := Sum(data); got != e.SHA256 {
		return nil, fmt.Errorf("preset %s does not match the registry index (sha256 %s, expected %s); nothing was installed", name, got, e.SHA256)
	}
	return data, nil
}
