package nearhostrc

import (
	"strings"
	"testing"
)

// Only the braced form is a variable in a .nearhostrc, so a "$" in a label
// stays as written, the braced forms and their defaults still fill, and an
// unset one reports its line.
func TestParseExpandsOnlyBracedVariables(t *testing.T) {
	data := []byte("version: 1\nroutes:\n  - port: ${APP_PORT}\n    label: \"Costs in $USD, ${CUR:-eur} $${APP_PORT}\"\n")
	f, err := Parse(data, map[string]string{"APP_PORT": "3000", "USD": "set"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Routes[0].Port != 3000 {
		t.Errorf("Port = %d, want 3000", f.Routes[0].Port)
	}
	if got, want := f.Routes[0].Label, "Costs in $USD, eur $3000"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}

	unset := []byte("version: 1\nroutes:\n  - port: 1\n    label: \"Costs in $USD\"\n  - port: 2\n    label: ${X}\n")
	_, err = Parse(unset, nil)
	if err == nil || !strings.Contains(err.Error(), "${X}") {
		t.Fatalf("err = %v, want one naming ${X}", err)
	}
	if got := Line(unset, err); got != 6 {
		t.Errorf("Line = %d, want 6", got)
	}
}
