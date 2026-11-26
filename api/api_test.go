package api

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The schema is a valid OpenAPI 3.1 document.
func TestSchemaParses(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(Schema)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
}
