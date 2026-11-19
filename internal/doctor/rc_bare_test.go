package doctor

import (
	"context"
	"strings"
	"testing"
)

// A bare $NAME in a port gets the hint and its line once in the message, and
// the finding's Line is the same line.
func TestRcFindingForABareVariableHasItsLineOnce(t *testing.T) {
	rc := "version: 1\nroutes:\n  - port: 80\n  - port: $APP_PORT\n"
	got := Check(context.Background(), Input{Files: files{".nearhostrc": rc}})
	f := byID(t, got, "nearhostrc.invalid")
	if f.Line != 4 || strings.Count(f.Message, "line 4") != 1 || !strings.Contains(f.Message, "Write a variable as ${APP_PORT}.") {
		t.Errorf("got %+v", f)
	}
}
