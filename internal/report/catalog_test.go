package report

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

var updateCatalog = flag.Bool("update", false, "rewrite catalog.json from the catalog")

// catalogFile is the catalog as the errors reference page reads it.
type catalogFile struct {
	// DocsBase is the page; a code's entry is its anchor.
	DocsBase string         `json:"docsBase"`
	Codes    []catalogEntry `json:"codes"`
	// Families are the codes made at run time, by prefix: a preset's
	// advice, advice.<id>.
	Families []catalogFamily `json:"families"`
}

type catalogFamily struct {
	Prefix  string `json:"prefix"`
	Level   Level  `json:"level"`
	Summary string `json:"summary"`
}

type catalogEntry struct {
	Code    Code   `json:"code"`
	Level   Level  `json:"level"`
	Status  int    `json:"status,omitempty"`
	Summary string `json:"summary"`
	Steps   []Step `json:"steps,omitempty"`
}

// catalogJSON is the catalog as catalog.json holds it: every code, in the
// order of codes.go, with its level, HTTP status, summary and, for a code
// whose fix never varies, its steps; then the advice family.
func catalogJSON(t *testing.T) []byte {
	t.Helper()
	f := catalogFile{DocsBase: DocsBase}
	for _, e := range Catalog() {
		f.Codes = append(f.Codes, catalogEntry{Code: e.Code, Level: e.Level, Status: e.Status, Summary: e.Summary, Steps: e.Steps})
	}
	advice, _ := Lookup(AdviceCode("id"))
	f.Families = []catalogFamily{{Prefix: adviceFamily, Level: advice.Level, Summary: advice.Summary}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestCatalogGolden keeps catalog.json, which the landing site's errors
// reference page is made from, in step with the catalog. Run
// `go test ./internal/report -run Golden -update` to rewrite it.
func TestCatalogGolden(t *testing.T) {
	got := catalogJSON(t)
	if *updateCatalog {
		if err := os.WriteFile("catalog.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	// A checkout on Windows may have turned the line endings.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatal("catalog.json is out of date; run `go test ./internal/report -run Golden -update` and check in the result")
	}
}

// TestFixedSteps checks the steps catalog entries declare: each is for a
// code of the catalog, says what it does, and has a nearhost command.
func TestFixedSteps(t *testing.T) {
	for c, steps := range fixedSteps {
		if _, ok := byCode[c]; !ok {
			t.Errorf("fixedSteps has %q, which is not in the catalog", c)
		}
		if len(steps) == 0 {
			t.Errorf("%q has no steps; leave it out of fixedSteps", c)
		}
		for _, s := range steps {
			if strings.TrimSpace(s.Text) == "" || !strings.HasPrefix(s.Command, "nearhost") {
				t.Errorf("%q has the step %+v; want text, then a nearhost command", c, s)
			}
		}
	}
	e, ok := Lookup(CoreNotRunning)
	if !ok || len(e.Steps) == 0 {
		t.Fatalf("Lookup(%q) has no steps", CoreNotRunning)
	}
	e.Steps[0].Command = "changed"
	if again, _ := Lookup(CoreNotRunning); again.Steps[0].Command == "changed" {
		t.Fatal("changing a looked-up entry's steps changed the catalog")
	}
}
