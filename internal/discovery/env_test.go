package discovery

import (
	"errors"
	"io/fs"
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
	env, err := readEnv(r)
	if err != nil {
		t.Fatal(err)
	}
	if env["APP_PORT"] != "80" || env["VITE_PORT"] != "5199" {
		t.Fatalf("unexpected env: %+v", env)
	}
}

func TestReadEnvWithNoFilesIsEmpty(t *testing.T) {
	env, err := readEnv(DirReader{Root: "testdata/empty"})
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
