package discovery

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
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

func TestParseEnvNamesTheBadLineByNumberAndKey(t *testing.T) {
	cases := []struct {
		name, data, want string
	}{
		{"dash in key", "APP_PORT=80\n# comment\nMY-PORT=3000\nDB_PASSWORD=hunter2\n", "line 3: MY-PORT could not be read"},
		{"crlf", "APP_PORT=80\r\n\r\nMY-PORT=3000\r\nDB_PASSWORD=hunter2\r\n", "line 3: MY-PORT could not be read"},
		{"export prefix", "export APP_PORT=80\nexport MY-PORT=3000\n", "line 2: MY-PORT could not be read"},
		{"yaml style", "A=1\nMY-PORT:3000\n", "line 2: MY-PORT could not be read"},
		{"no separator", "A=1\nB=2\nOOPS hunter-2\n", "line 3: OOPS could not be read"},
		{"long key", "A=1\n" + strings.Repeat("K", 50) + "-X=1\n", "line 2: " + strings.Repeat("K", 40) + " could not be read"},
		{"control characters", "A=1\nMY\x01-PORT=1\n", "line 2: MY-PORT could not be read"},
		{"unterminated quote", "A=1\nB=2\nDB_PASSWORD=\"hunter2\nC=3\n", "line 3: DB_PASSWORD could not be read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseEnv([]byte(c.data))
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestParseEnvErrorNeverShowsValues(t *testing.T) {
	const secret = "hunter2"
	files := []string{
		"SECRET=hunter2\nMY-PORT=3000\nTOKEN=hunter2xyz\nPASSWORD=\"hunter2\"\n",
		"MY-PORT=hunter2\nSECRET=hunter2\n",
		"A=1\nMY-PORT: hunter2\nSECRET=hunter2\n",
		"A=1\nBAD KEY-hunter2=x\nSECRET=hunter2\n",
		"SECRET=hunter2\nPASSWORD=\"hunter2\nTOKEN=hunter2\n",
		"PASSWORD='hunter2\nSECRET=hunter2\n",
		"SECRET=\"hunter2\"\nOTHER=\"hunter2\nMORE=\"hunter2\n",
		"SECRET=hunter2\n\xEF\xBB\xBFBOM=hunter2\n",
		"SECRET=hunter2\n\"QUOTED\"=hunter2\n",
	}
	for i, data := range files {
		_, err := ParseEnv([]byte(data))
		if err == nil {
			t.Fatalf("file %d: expected an error", i)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("file %d: error shows a value: %v", i, err)
		}
	}
}

func TestReadEnvNamesTheFileAndLine(t *testing.T) {
	_, err := ReadEnv(memReader{".env": "A=1\nMY-PORT=3000\nSECRET=hunter2\n"})
	if err == nil || err.Error() != ".env: line 2: MY-PORT could not be read" {
		t.Fatalf("got %v", err)
	}
}

func TestParseEnvWithoutAKnownLineSaysOnlyThatItCouldNotBeRead(t *testing.T) {
	files := []string{
		// The error starts inside a quoted value that ran past its line.
		"SECRET=\"hunter2\"\nOTHER=\"hunter2\nMORE=\"hunter2\n",
	}
	for i, data := range files {
		_, err := ParseEnv([]byte(data))
		if err == nil || err.Error() != "could not be read" {
			t.Fatalf("file %d: got %v", i, err)
		}
	}
	if line, key := badLine("A=1\n", "zero length string"); line != 0 || key != "" {
		t.Fatalf("unknown error form gave line %d key %q", line, key)
	}
}
