package discovery

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
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

func TestEnvCandidatesMapSailVariables(t *testing.T) {
	env, _ := readEnv(DirReader{Root: "testdata/sail-custom"})
	cands := envCandidates(env)
	want := map[int]struct {
		role  config.Role
		label string
	}{
		8080: {config.RoleWeb, "Laravel (APP_PORT)"},
		5174: {config.RoleLoopback, "Vite (VITE_PORT)"},
		3307: {config.RoleIgnore, "Database (FORWARD_DB_PORT)"},
	}
	if len(cands) != len(want) {
		t.Fatalf("expected %d candidates, got %+v", len(want), cands)
	}
	for _, c := range cands {
		w, ok := want[c.Port]
		if !ok || c.Role != w.role || c.Label != w.label || c.Source != "env:"+c.Var {
			t.Errorf("unexpected candidate %+v", c)
		}
	}
}

func TestEnvCandidatesSkipNonNumeric(t *testing.T) {
	cands := envCandidates(map[string]string{"APP_PORT": "eighty", "VITE_PORT": ""})
	if len(cands) != 0 {
		t.Fatalf("expected none, got %+v", cands)
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
