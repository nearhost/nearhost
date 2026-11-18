package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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

// maxKeyLabel is the most characters of a key an error shows.
const maxKeyLabel = 40

// ParseEnv parses dotenv content. When the content cannot be parsed, the error
// names the failing line and its key, and never any value or other line,
// since a .env holds passwords and API keys.
func ParseEnv(data []byte) (map[string]string, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	env, err := godotenv.UnmarshalBytes(data)
	if err != nil {
		return nil, envError(data, err)
	}
	return env, nil
}

// envError turns a godotenv error into one that names the failing line and,
// when it is key-shaped, its key. godotenv's own text quotes the rest of the file, so it is never shown.
func envError(data []byte, err error) error {
	src := strings.ReplaceAll(string(data), "\r\n", "\n") // as godotenv sees it
	line, key := badLine(src, err.Error())
	if line == 0 {
		return errors.New("could not be read")
	}
	if key == "" {
		return fmt.Errorf("line %d could not be read", line)
	}
	return fmt.Errorf("line %d: %s could not be read", line, key)
}

// badLine finds the line godotenv failed on, and its key, from godotenv's
// error text. It returns 0 when the text has no form it knows.
func badLine(src, msg string) (int, string) {
	if near, ok := nearText(msg); ok {
		// near is the rest of the input from the bad statement on.
		if !strings.HasSuffix(src, near) {
			return 0, ""
		}
		start := len(src) - len(near)
		if !atStatementStart(src[:start]) {
			// It starts inside a quoted value that ran past its line.
			return 0, ""
		}
		return 1 + strings.Count(src[:start], "\n"), keyLabel(near)
	}
	if value, ok := strings.CutPrefix(msg, "unterminated quoted value "); ok {
		// value is the bad statement's value up to the end of its line.
		start, ok := unterminatedStart(src, value)
		if !ok {
			return 0, ""
		}
		return 1 + strings.Count(src[:start], "\n"), keyLabel(trimExport(src[start:]))
	}
	return 0, ""
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

// unterminatedStart finds the start of the one line whose value is value: it
// follows "=" or ":" and runs to the end of the line. It reports false when no
// line or more than one line matches.
func unterminatedStart(src, value string) (int, bool) {
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

// trimExport removes indentation and an "export" prefix from the start of a
// statement.
func trimExport(text string) string {
	text = strings.TrimLeft(text, " \t")
	if rest, ok := strings.CutPrefix(text, "export"); ok && strings.IndexAny(rest, " \t") == 0 {
		text = strings.TrimLeft(rest, " \t")
	}
	return text
}

// validKeyLine reports whether head, a line up to its value, is a key godotenv
// accepts followed by "=" or ":", so the line is a statement and not the inside
// of a quoted value.
func validKeyLine(head string) bool {
	head = strings.TrimRight(trimExport(head), " \t")
	key, ok := strings.CutSuffix(head, "=")
	if !ok {
		key, ok = strings.CutSuffix(head, ":")
	}
	key = strings.TrimRight(key, " \t")
	if !ok || key == "" {
		return false
	}
	for _, r := range key {
		if r != '_' && r != '.' && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

// keyLabel returns the key of the statement at the start of text, at most
// maxKeyLabel characters, with control and format characters removed so a
// terminal shows it as written. It returns "" unless the line has a "=" or
// ":" separator and the text before it is key-shaped, since a line without
// one may be a token pasted on its own.
func keyLabel(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	sep := strings.IndexAny(line, "=:")
	if sep < 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.TrimRight(line[:sep], " \t") {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			continue
		}
		if r != '_' && r != '-' && r != '.' && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return ""
		}
		b.WriteRune(r)
	}
	key := []rune(b.String())
	if len(key) > maxKeyLabel {
		key = key[:maxKeyLabel]
	}
	return string(key)
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
