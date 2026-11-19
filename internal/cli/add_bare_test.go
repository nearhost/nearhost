package cli

import (
	"path/filepath"
	"testing"
)

// nearhost add shows a bare $NAME in a port with its line and the way to
// write it.
func TestAddNamesTheLineOfABareVariable(t *testing.T) {
	dir := rcProject(t, "version: 1\nroutes:\n  - port: $APP_PORT\n")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := executeWithInput(t, "", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--no-presets")
	if err == nil {
		t.Fatalf("add succeeded:\n%s", out)
	}
	want := `.nearhostrc line 3: port "$APP_PORT" is not a number from 1 to 65535. Write a variable as ${APP_PORT}.`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}
