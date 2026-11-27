package control

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/nearhost/nearhost/api"
)

// loadSchema returns the control API's schema.
func loadSchema(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(api.Schema)
	if err != nil {
		t.Fatalf("load the schema: %v", err)
	}
	return doc
}

// Every route the server registers is in the schema with the same method
// and path, and the schema has no other.
func TestSchemaRoutesMatchTheMux(t *testing.T) {
	s := &Server{b: newActionFake(), hub: NewHub()}
	s.routes()
	mux := map[string]bool{}
	for _, p := range s.Patterns() {
		if mux[p] {
			t.Errorf("pattern %q is registered twice", p)
		}
		mux[p] = true
	}
	if len(mux) == 0 {
		t.Fatal("the server registers no patterns")
	}
	schema := map[string]bool{}
	for path, item := range loadSchema(t).Paths.Map() {
		for method := range item.Operations() {
			schema[strings.ToUpper(method)+" "+path] = true
		}
	}
	var noSchema, noMux []string
	for p := range mux {
		if !schema[p] {
			noSchema = append(noSchema, p)
		}
	}
	for p := range schema {
		if !mux[p] {
			noMux = append(noMux, p)
		}
	}
	slices.Sort(noSchema)
	slices.Sort(noMux)
	if len(noSchema) > 0 {
		t.Errorf("routes missing from the schema:\n  %s", strings.Join(noSchema, "\n  "))
	}
	if len(noMux) > 0 {
		t.Errorf("schema routes the server does not register:\n  %s", strings.Join(noMux, "\n  "))
	}
}

// Every operation has an operationId, used once.
func TestOperationIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for path, item := range loadSchema(t).Paths.Map() {
		for method, op := range item.Operations() {
			where := method + " " + path
			if op.OperationID == "" {
				t.Errorf("%s has no operationId", where)
				continue
			}
			if other, ok := seen[op.OperationID]; ok {
				t.Errorf("operationId %q is used by %s and %s", op.OperationID, other, where)
			}
			seen[op.OperationID] = where
		}
	}
}

// Patterns returns a copy, which the caller may change.
func TestPatternsIsACopy(t *testing.T) {
	s := &Server{b: newActionFake(), hub: NewHub()}
	s.routes()
	p := s.Patterns()
	p[0] = "changed"
	if s.Patterns()[0] == "changed" {
		t.Error("Patterns returns the server's own slice")
	}
	if got := s.Patterns()[0]; got != http.MethodGet+" /v1" {
		t.Errorf("first pattern = %q, want GET /v1", got)
	}
}

// The schema's version is the API version, not the release.
func TestSchemaVersionIsTheAPIVersion(t *testing.T) {
	if got, want := loadSchema(t).Info.Version, strconv.Itoa(APIVersion); got != want {
		t.Errorf("info.version = %q, want %q", got, want)
	}
}

// The schema is served as it is embedded.
func TestSchemaIsServed(t *testing.T) {
	dir := serve(t, newFake())
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(dir))
		},
	}}
	defer hc.CloseIdleConnections()
	resp, err := hc.Get("http://nearhost/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("Content-Type = %q, want application/yaml", ct)
	}
	if !bytes.Equal(body, api.Schema) {
		t.Fatalf("the body is not the embedded schema (%d bytes, want %d)", len(body), len(api.Schema))
	}
}
