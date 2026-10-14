package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ref = "3f2c1ab9d0e4"

// site is a registry served from memory.
type site struct {
	files map[string]string
	hits  map[string]int
	down  bool
}

func serve(t *testing.T) (*site, *Client) {
	t.Helper()
	s := &site{files: map[string]string{}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits[r.URL.Path]++
		body, ok := s.files[r.URL.Path]
		if s.down || !ok {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return s, &Client{BaseURL: srv.URL, Dir: filepath.Join(t.TempDir(), "registry"), HTTP: srv.Client(), Now: time.Now}
}

// publish puts presets and their index on the site.
func (s *site) publish(ref string, presets map[string]string) {
	var b strings.Builder
	fmt.Fprintf(&b, "schema: 1\nref: %s\npresets:\n", ref)
	for name, body := range presets {
		s.files["/"+ref+"/presets/"+name+".yaml"] = body
		fmt.Fprintf(&b, "  - name: %s\n    title: %s title\n    preset_schema: 1\n    detect: file(\"%s.config\")\n    sha256: '%s'\n", name, name, name, Sum([]byte(body)))
	}
	s.files["/main/index.yaml"] = b.String()
}

func TestIndexIsFetchedOnceThenServedFromTheCache(t *testing.T) {
	s, c := serve(t)
	s.publish(ref, map[string]string{"stack": "one"})
	for i := 0; i < 3; i++ {
		idx, err := c.Index(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		e, ok := idx.Entry("stack")
		if idx.Ref != ref || !ok || e.Title != "stack title" || e.Detect != `file("stack.config")` || e.PresetSchema != 1 {
			t.Fatalf("index = %+v", idx)
		}
	}
	if s.hits["/main/index.yaml"] != 1 {
		t.Errorf("the index was fetched %d times, want once", s.hits["/main/index.yaml"])
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "index.yaml")); err != nil {
		t.Errorf("the index was not cached: %v", err)
	}
}

func TestIndexIsRefreshedAfterADay(t *testing.T) {
	s, c := serve(t)
	s.publish(ref, map[string]string{"stack": "one"})
	if _, err := c.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.publish("aaaaaaa", map[string]string{"stack": "two"})

	c.Now = func() time.Time { return time.Now().Add(23 * time.Hour) }
	if idx, _ := c.Index(context.Background()); idx.Ref != ref {
		t.Errorf("after 23 hours the cached index should serve, got ref %s", idx.Ref)
	}
	c.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if idx, _ := c.Index(context.Background()); idx.Ref != "aaaaaaa" {
		t.Errorf("after 25 hours the index should be fetched again, got ref %s", idx.Ref)
	}
}

func TestIndexFallsBackToAStaleCopy(t *testing.T) {
	s, c := serve(t)
	s.publish(ref, map[string]string{"stack": "one"})
	if _, err := c.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Now().Add(72 * time.Hour) }

	s.down = true
	if idx, err := c.Index(context.Background()); err != nil || idx.Ref != ref {
		t.Fatalf("with the registry down the stale copy should serve: %+v %v", idx, err)
	}
	s.down = false
	s.files["/main/index.yaml"] = "schema: 1\nref: not a commit\n"
	if idx, err := c.Index(context.Background()); err != nil || idx.Ref != ref {
		t.Fatalf("a bad index must not replace a good copy: %+v %v", idx, err)
	}
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Error("Refresh has no fallback and should fail on a bad index")
	}
}

func TestIndexUnavailableWithNoCopy(t *testing.T) {
	s, c := serve(t)
	s.down = true
	if _, err := c.Index(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	c.BaseURL = "http://127.0.0.1:1"
	if _, err := c.Index(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for a refused connection, got %v", err)
	}
}

// A registry that answers with an index this build cannot read was reached;
// the reason it cannot be used is that nearhost is too old.
func TestIndexNewerThanThisBuildWithNoCopy(t *testing.T) {
	s, c := serve(t)
	s.files["/main/index.yaml"] = "schema: 2\nref: " + ref + "\n"
	_, err := c.Index(context.Background())
	if !errors.Is(err, ErrNewerIndex) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrNewerIndex and not ErrUnavailable, got %v", err)
	}
}

// The cause of an unavailable registry stays in the error chain.
func TestIndexUnavailableKeepsTheCause(t *testing.T) {
	_, c := serve(t)
	c.BaseURL = "http://127.0.0.1:1"
	_, err := c.Index(context.Background())
	var opErr *net.OpError
	if !errors.Is(err, ErrUnavailable) || !errors.As(err, &opErr) {
		t.Fatalf("expected ErrUnavailable wrapping the network error, got %v", err)
	}
}

func TestFetchChecksTheHash(t *testing.T) {
	s, c := serve(t)
	s.publish(ref, map[string]string{"stack": "the preset"})
	idx, err := c.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Fetch(context.Background(), idx, "stack")
	if err != nil || string(data) != "the preset" {
		t.Fatalf("Fetch = %q %v", data, err)
	}
	if s.hits["/"+ref+"/presets/stack.yaml"] != 1 {
		t.Errorf("the preset should come from the index's ref: %v", s.hits)
	}

	s.files["/"+ref+"/presets/stack.yaml"] = "tampered"
	if _, err := c.Fetch(context.Background(), idx, "stack"); err == nil || !strings.Contains(err.Error(), "does not match the registry index") {
		t.Errorf("expected a hash error, got %v", err)
	}
	if _, err := c.Fetch(context.Background(), idx, "other"); err == nil || !strings.Contains(err.Error(), `no preset named "other"`) {
		t.Errorf("expected an unknown preset error, got %v", err)
	}
	delete(s.files, "/"+ref+"/presets/stack.yaml")
	if _, err := c.Fetch(context.Background(), idx, "stack"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected the HTTP status, got %v", err)
	}
}

func TestFetchRefusesAnOversizedResponse(t *testing.T) {
	s, c := serve(t)
	big := strings.Repeat("x", maxBody+1)
	s.publish(ref, map[string]string{"stack": big})
	idx, err := c.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(context.Background(), idx, "stack"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("expected a size error, got %v", err)
	}
}

func TestParseIndexRejectsWhatCouldMislead(t *testing.T) {
	sum := Sum([]byte("x"))
	entry := func(name, sha string) string {
		return "schema: 1\nref: " + ref + "\npresets:\n  - {name: '" + name + "', title: T, preset_schema: 1, detect: 'true', sha256: '" + sha + "'}\n"
	}
	for name, doc := range map[string]string{
		"not yaml":               "{",
		"no schema":              "ref: " + ref + "\n",
		"newer schema":           "schema: 2\nref: " + ref + "\n",
		"ref with a path":        "schema: 1\nref: ../main\n",
		"name with path":         entry("../../etc/passwd", sum),
		"upper case name":        entry("Stack", sum),
		"short hash":             entry("stack", "abc"),
		"no detect":              "schema: 1\nref: " + ref + "\npresets:\n  - {name: stack, title: T, sha256: " + sum + "}\n",
		"listed twice":           "schema: 1\nref: " + ref + "\npresets:\n  - {name: a, detect: 'true', sha256: " + sum + "}\n  - {name: a, detect: 'true', sha256: " + sum + "}\n",
		"control in title":       "schema: 1\nref: " + ref + "\npresets:\n  - {name: a, title: \"A\\e[2J\", detect: 'true', sha256: " + sum + "}\n",
		"control in description": "schema: 1\nref: " + ref + "\npresets:\n  - {name: a, title: A, description: \"B\\nC\", detect: 'true', sha256: " + sum + "}\n",
	} {
		if _, err := ParseIndex([]byte(doc)); err == nil {
			t.Errorf("%s: ParseIndex accepted it", name)
		}
	}
	if idx, err := ParseIndex([]byte(entry("stack", sum) + "future_key: 1\n")); err != nil || len(idx.Presets) != 1 {
		t.Errorf("a valid index with a key this build does not know should load: %v", err)
	}
	if _, err := ParseIndex([]byte("schema: 2\nref: " + ref + "\n")); !errors.Is(err, ErrNewerIndex) || !strings.Contains(err.Error(), "newer nearhost") {
		t.Errorf("a newer index should say to upgrade: %v", err)
	}
}

func TestNewPointsAtTheOfficialRegistry(t *testing.T) {
	c := New("/tmp/x")
	if c.BaseURL != "https://raw.githubusercontent.com/nearhost/presets" || c.Dir != "/tmp/x" || c.HTTP == nil || c.Now == nil {
		t.Errorf("New = %+v", c)
	}
}
