package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDirReaderReturnsNotExistForMissingFile(t *testing.T) {
	r := DirReader{Root: "testdata/empty"}
	_, err := r.ReadFile(".env")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestParseEnvHandlesQuotesAndPadding(t *testing.T) {
	env, err := ParseEnv([]byte("APP_PORT= 8080\nVITE_PORT=\"5174\"\nAPP_URL='http://x'\n# comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	if env["VITE_PORT"] != "5174" || env["APP_URL"] != "http://x" {
		t.Fatalf("unexpected env: %+v", env)
	}
}

func TestReadEnvAppliesLocalOverrides(t *testing.T) {
	r := memReader{
		".env":       "APP_PORT=80\nVITE_PORT=5173\n",
		".env.local": "VITE_PORT=5199\n",
	}
	env, err := ReadEnv(r)
	if err != nil {
		t.Fatal(err)
	}
	if env["APP_PORT"] != "80" || env["VITE_PORT"] != "5199" {
		t.Fatalf("unexpected env: %+v", env)
	}
}

func TestReadEnvWithNoFilesIsEmpty(t *testing.T) {
	env, err := ReadEnv(DirReader{Root: "testdata/empty"})
	if err != nil || len(env) != 0 {
		t.Fatalf("expected empty env, got %+v %v", env, err)
	}
}

// memReader is an in-memory FileReader for tests.
type memReader map[string]string

func (m memReader) ReadFile(name string) ([]byte, error) {
	s, ok := m[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

func TestParseEnvStripsByteOrderMark(t *testing.T) {
	env, err := ParseEnv([]byte("\xEF\xBB\xBFAPP_PORT=8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	if env["APP_PORT"] != "8080" {
		t.Fatalf("unexpected env: %+v", env)
	}
}

// errorForm is the only shape a ParseEnv error may take.
var errorForm = regexp.MustCompile(`^(line [0-9]+ )?could not be read$`)

// malformedEnv is a .env that godotenv rejects, the line ParseEnv should
// name (0 for none), and a secret the error must not show.
type malformedEnv struct {
	name, data string
	line       int
	secret     string
}

// malformedEnvs covers each error form godotenv has, with a secret on other
// lines, as the bad line's value and as the bad line itself.
var malformedEnvs = []malformedEnv{
	{"dash in key", "APP_PORT=80\n# comment\nMY-PORT=3000\nDB_PASSWORD=hunter2\n", 3, "hunter2"},
	{"crlf", "APP_PORT=80\r\n\r\nMY-PORT=3000\r\nDB_PASSWORD=hunter2\r\n", 3, "hunter2"},
	{"export prefix", "export APP_PORT=80\nexport MY-PORT=hunter2\n", 2, "hunter2"},
	{"yaml style", "A=1\nMY-PORT: hunter2\nSECRET=hunter2\n", 2, "hunter2"},
	{"no separator", "A=1\nB=2\nOOPS hunter-2\n", 3, "hunter-2"},
	{"key is the secret", "A=1\nhunter2-key=x\n", 2, "hunter2"},
	{"control characters", "A=1\nMY\x01-PORT=hunter2\n", 2, "hunter2"},
	{"pasted token", "A=1\nghp_hunter2token\n", 2, "ghp_hunter2token"},
	// godotenv accepts spaces in a key, so the dash makes this line fail.
	{"spaces in key", "A=1\nmy key-with spaces=hunter2\n", 2, "hunter2"},
	{"quoted key", "SECRET=hunter2\n\"QUOTED\"=hunter2\n", 2, "hunter2"},
	{"bom inside", "SECRET=hunter2\n\xEF\xBB\xBFBOM=hunter2\n", 2, "hunter2"},
	{"token before cr", "A=1\nsk-live-abc123\rB=2\n", 2, "sk-live-abc123"},
	{"token before vt", "A=1\nsk-live-abc123\vB=2\n", 2, "sk-live-abc123"},
	{"token before ff", "A=1\nsk-live-abc123\fB=2\n", 2, "sk-live-abc123"},
	{"padded base64", "A=1\nZmDfcTF7-60GrrY167zsiPd67pEvs0aGOv2oasOM1Pg=\n", 2, "ZmDfcTF7"},
	{"unterminated quote", "A=1\nB=2\nDB_PASSWORD=\"hunter2\nC=3\n", 3, "hunter2"},
	{"unterminated single quote", "PASSWORD='hunter2\nSECRET=hunter2\n", 1, "hunter2"},
	{"unterminated after secrets", "SECRET=hunter2\nPASSWORD=\"hunter2\nTOKEN=hunter2\n", 2, "hunter2"},
	{"unterminated after a multi-line value", "A='x\nB=\"hunter2\n'\nMY KEY=\"hunter2\n", 4, "hunter2"},
	// godotenv skips \v, \f and \r after the separator and fails on line 3,
	// since line 1's quote closes on line 2. Lines 1 and 3 both fit, so no
	// line is named; line 1 must never be.
	{"vertical tab before quote", "A=\"hunter2\nB=\"\nC=\v\"hunter2\n", 0, "hunter2"},
	{"form feed before quote", "A=\"hunter2\nB=\"\nC=\f\"hunter2\n", 0, "hunter2"},
	{"carriage return before quote", "A=\"hunter2\nB=\"\nC=\r\"hunter2\n", 0, "hunter2"},
	// The error starts inside a quoted value that ran past its line.
	{"quote runs past its line", "SECRET=\"hunter2\"\nOTHER=\"hunter2\nMORE=\"hunter2\n", 0, "hunter2"},
}

func TestParseEnvNamesOnlyTheBadLine(t *testing.T) {
	for _, c := range malformedEnvs {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseEnv([]byte(c.data))
			want := "could not be read"
			if c.line > 0 {
				want = fmt.Sprintf("line %d could not be read", c.line)
			}
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

func TestParseEnvErrorShowsNothingFromTheFile(t *testing.T) {
	for _, c := range malformedEnvs {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseEnv([]byte(c.data))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), c.secret) {
				t.Fatalf("error shows %q: %v", c.secret, err)
			}
			if !errorForm.MatchString(err.Error()) {
				t.Fatalf("error %q is not of the form %s", err, errorForm)
			}
		})
	}
}

func TestReadEnvNamesTheFileAndLine(t *testing.T) {
	_, err := ReadEnv(memReader{".env": "A=1\nB=2\nMY-PORT=3000\nSECRET=hunter2\n"})
	if err == nil || err.Error() != ".env: line 3 could not be read" {
		t.Fatalf("got %v", err)
	}
}

func TestParseEnvDoesNotPlaceAnUnterminatedQuoteInALargeFile(t *testing.T) {
	big := strings.Repeat("A=1\n", maxPlaceSize/4+1) + "B=\"hunter2\n"
	many := "X='\n" + strings.Repeat("K=\"abc\n", maxPlaceCandidates+1) + "'\nT=\"abc\n"
	for name, data := range map[string]string{"over the size limit": big, "over the candidate limit": many} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := ParseEnv([]byte(data))
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || err.Error() != "could not be read" {
					t.Fatalf("got %v, want could not be read", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ParseEnv took longer than 5s")
			}
		})
	}
}

func TestBadLineWithAnUnknownErrorFormIsZero(t *testing.T) {
	if line := badLine("A=1\n", errors.New("zero length string")); line != 0 {
		t.Fatalf("got line %d", line)
	}
}
