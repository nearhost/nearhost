package nearhostrc

import (
	"strings"
	"testing"
)

// A bare $NAME in a port or host used to fill and now stays as text, so the
// error says to write ${NAME} and Line points at the literal text.
func TestParseBareVariableErrorsSayWhatToWrite(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
		line int
	}{
		{"port", "version: 1\nroutes:\n  - port: 3000\n  - port: $APP_PORT\n", `port "$APP_PORT" is not a number from 1 to 65535. Write a variable as ${APP_PORT}.`, 4},
		{"host", "version: 1\nroutes:\n  - port: 3000\n    hosts: [$HOST.test]\n", `Write a variable as ${HOST}.`, 4},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.data), map[string]string{"APP_PORT": "3000", "HOST": "app"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
			continue
		}
		if got := Line([]byte(c.data), err); got != c.line {
			t.Errorf("%s: Line = %d, want %d", c.name, got, c.line)
		}
	}
}

// A "$" that does not start a name gets no hint.
func TestParseNoHintForPlainDollar(t *testing.T) {
	_, err := Parse([]byte("version: 1\nroutes:\n  - port: \"$1\"\n"), nil)
	if err == nil || strings.Contains(err.Error(), "Write a variable") {
		t.Fatalf("err = %v, want one without the hint", err)
	}
}
