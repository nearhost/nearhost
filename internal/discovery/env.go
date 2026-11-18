package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode"

	"github.com/joho/godotenv"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

// Candidate is a discovered port with a proposed role.
type Candidate struct {
	Port   int         // port published on the server
	Target int         // container port when known, else 0
	Role   config.Role // proposed role
	Label  string      // human label: the Compose service, or a preset's label and variable
	Source string      // where it came from: "compose:<service>", "env:<variable>", "preset:<key>"
	Var    string      // environment variable that sets it, if any
	Preset string      // key of the preset that identified it, if any
	Hosts  []string    // names a preset says it is served at; only a web route keeps them

	web preset.WebHint
}

// utf8BOM is the byte order mark some editors write at the start of a file.
var utf8BOM = []byte("\xEF\xBB\xBF")

// ParseEnv parses dotenv content. When the content cannot be parsed, the error
// names at most the failing line's number. Nothing from the file appears in
// it, since a .env holds passwords and API keys.
func ParseEnv(data []byte) (map[string]string, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	env, err := godotenv.UnmarshalBytes(data)
	if err != nil {
		return nil, envError(data, err)
	}
	return env, nil
}

// envError turns a godotenv error into one that names only the failing line.
// godotenv's own text quotes the rest of the file, so it is never shown.
func envError(data []byte, err error) error {
	src := strings.ReplaceAll(string(data), "\r\n", "\n") // as godotenv sees it
	line := badLine(src, err)
	if line == 0 {
		return errors.New("could not be read")
	}
	return fmt.Errorf("line %d could not be read", line)
}

// badLine finds the line godotenv failed on from its error. It returns 0 when
// the error has no form it knows or the line cannot be placed for certain.
func badLine(src string, err error) int {
	msg := err.Error()
	if near, ok := nearText(msg); ok {
		// near is the rest of the input from the bad statement on.
		if !strings.HasSuffix(src, near) {
			return 0
		}
		start := len(src) - len(near)
		if !atStatementStart(src[:start]) {
			// It starts inside a quoted value that ran past its line.
			return 0
		}
		return 1 + strings.Count(src[:start], "\n")
	}
	if value, ok := strings.CutPrefix(msg, "unterminated quoted value "); ok {
		// value is the bad statement's value up to the end of its line.
		start, ok := unterminatedStart(src, value, msg)
		if !ok {
			return 0
		}
		return 1 + strings.Count(src[:start], "\n")
	}
	return 0
}

// nearText reads the quoted remainder from godotenv's "unexpected character
// %q in variable name near %q" error.
func nearText(msg string) (string, bool) {
	rest, ok := strings.CutPrefix(msg, "unexpected character ")
	if !ok {
		return "", false
	}
	char, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", false
	}
	quoted, ok := strings.CutPrefix(rest[len(char):], " in variable name near ")
	if !ok {
		return "", false
	}
	near, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return near, true
}

// unterminatedStart finds the start of the line whose unterminated quoted
// value godotenv reported. A candidate line has a key, then "=" or ":", then
// value up to the end of the line. Because a candidate can sit inside an
// earlier quoted value that spans lines, a candidate counts only when the
// input before it parses and the input from it fails with the same error,
// which is what godotenv saw. It reports false unless exactly one line counts.
func unterminatedStart(src, value, msg string) (int, bool) {
	found := -1
	for from := 0; ; {
		i := strings.Index(src[from:], value)
		if i < 0 {
			break
		}
		at := from + i
		from = at + 1
		end := at + len(value)
		if end < len(src) && src[end] != '\n' {
			continue
		}
		lineStart := strings.LastIndex(src[:at], "\n") + 1
		if !validKeyLine(src[lineStart:at]) {
			continue
		}
		if _, err := godotenv.Unmarshal(src[:lineStart]); err != nil {
			continue
		}
		if _, err := godotenv.Unmarshal(src[lineStart:]); err == nil || err.Error() != msg {
			continue
		}
		if found >= 0 {
			return 0, false
		}
		found = lineStart
	}
	return found, found >= 0
}

// atStatementStart reports whether before, the input ahead of a statement,
// ends at the start of a line, allowing indentation and an "export" prefix.
func atStatementStart(before string) bool {
	before = strings.TrimRight(before, " \t")
	before = strings.TrimSuffix(before, "export")
	before = strings.TrimRight(before, " \t")
	return before == "" || strings.HasSuffix(before, "\n")
}

// validKeyLine reports whether head, a line up to its value, is a key godotenv
// accepts followed by "=" or ":", so the line can be a statement.
func validKeyLine(head string) bool {
	head = strings.TrimLeft(head, " \t")
	if rest, ok := strings.CutPrefix(head, "export"); ok && strings.IndexAny(rest, " \t") == 0 {
		head = rest
	}
	head = strings.TrimRight(head, " \t")
	key, ok := strings.CutSuffix(head, "=")
	if !ok {
		key, ok = strings.CutSuffix(head, ":")
	}
	key = strings.Trim(key, " \t")
	if !ok || key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsNumber(r):
		case r == ' ' || r == '\t' || r == '\v' || r == '\f' || r == '\r':
		default:
			return false
		}
	}
	return true
}

// ReadEnv reads .env then .env.local, later files overriding earlier ones.
func ReadEnv(r FileReader) (map[string]string, error) {
	merged := map[string]string{}
	for _, name := range []string{".env", ".env.local"} {
		data, err := r.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		env, err := ParseEnv(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for k, v := range env {
			merged[k] = v
		}
	}
	return merged, nil
}
