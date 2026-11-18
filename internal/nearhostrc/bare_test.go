package nearhostrc

import (
	"strings"
	"testing"
)

// A bare $NAME in a port or host used to fill and now stays as text, so the
// error carries its line, says to write ${NAME}, and Line returns the line of
// the field that failed, not of an earlier mention of the same text.
func TestParseBareVariableErrorsSayWhatToWrite(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
		line int
	}{
		{"port", "version: 1\nroutes:\n  - port: 3000\n  - port: $APP_PORT\n",
			`.nearhostrc line 4: port "$APP_PORT" is not a number from 1 to 65535. Write a variable as ${APP_PORT}.`, 4},
		{"host", "version: 1\nroutes:\n  - port: 3000\n    hosts: [$HOST.test]\n",
			`.nearhostrc line 4: invalid host "$HOST.test"`, 4},
		{"after a label with the same text", "version: 1\n# $APP_PORT\nroutes:\n  - port: 3000\n    label: costs $APP_PORT\n  - port: $APP_PORT\n",
			`.nearhostrc line 6: port "$APP_PORT" is not a number from 1 to 65535. Write a variable as ${APP_PORT}.`, 6},
		{"alias", "version: 1\nalias: $NAME.test\nroutes: []\n",
			`.nearhostrc line 2: alias: `, 2},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.data), map[string]string{"APP_PORT": "3000", "HOST": "app"})
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "Write a variable as ${") {
			t.Errorf("%s: err = %v, want it to contain %q and the hint", c.name, err, c.want)
			continue
		}
		if got := Line([]byte(c.data), err); got != c.line {
			t.Errorf("%s: Line = %d, want %d", c.name, got, c.line)
		}
	}
}

// A missing ${NAME} names its line in the message, as a bare one does.
func TestParseUnsetVariableMessageHasItsLine(t *testing.T) {
	data := []byte("version: 1\nroutes:\n  - port: 3000\n    hosts: [a.test, ${MISSING}]\n")
	_, err := Parse(data, nil)
	want := ".nearhostrc line 4: ${MISSING} has no value in .env.local or .env, and no default"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := Line(data, err); got != 4 {
		t.Errorf("Line = %d, want 4", got)
	}
}

// A "$" that does not start a name gets no hint, and neither does a value
// from .env that holds one: the hint reads the text as written.
func TestParseNoHintWithoutABareName(t *testing.T) {
	for name, c := range map[string]struct {
		data string
		env  map[string]string
	}{
		"plain dollar":    {"version: 1\nroutes:\n  - port: \"$1\"\n", nil},
		"value from .env": {"version: 1\nroutes:\n  - port: ${P}\n", map[string]string{"P": "$foo"}},
	} {
		_, err := Parse([]byte(c.data), c.env)
		if err == nil || strings.Contains(err.Error(), "Write a variable") {
			t.Errorf("%s: err = %v, want one without the hint", name, err)
		}
	}
}
