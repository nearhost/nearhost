package grants

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tempStore opens a store on a file in a directory that does not exist yet.
func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nearhost", "grants.json")
	return Open(path), path
}

func TestAllowThenCheck(t *testing.T) {
	s, _ := tempStore(t)
	token, err := s.Allow("apollo", "claude-code", KindPassthrough)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("token has %d characters, want 43", len(token))
	}
	for _, c := range []struct {
		name, server, requester, token string
		want                           bool
	}{
		{"right token", "apollo", "claude-code", token, true},
		{"empty token", "apollo", "claude-code", "", false},
		// "!" is not in the base64url alphabet, so this always differs.
		{"other token", "apollo", "claude-code", "!" + token[1:], false},
		{"other server", "mini", "claude-code", token, false},
		{"other requester", "apollo", "codex", token, false},
	} {
		got, err := s.Check(c.server, c.requester, KindPassthrough, c.token)
		if err != nil || got != c.want {
			t.Errorf("%s: Check = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestAllowReplacesToken(t *testing.T) {
	s, _ := tempStore(t)
	first, err := s.Allow("apollo", "claude-code", KindPassthrough)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Allow("apollo", "claude-code", KindPassthrough)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Check("apollo", "claude-code", KindPassthrough, first); ok {
		t.Error("the first token still works")
	}
	if ok, _ := s.Check("apollo", "claude-code", KindPassthrough, second); !ok {
		t.Error("the second token does not work")
	}
	list, err := s.List("apollo")
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v; want one grant", list, err)
	}
}

func TestFileHoldsNoToken(t *testing.T) {
	s, path := tempStore(t)
	token, _ := s.Allow("apollo", "claude-code", KindPassthrough)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if strings.Contains(string(data), token) {
		t.Error("the file holds the token")
	}
	if !strings.Contains(string(data), hex.EncodeToString(sum[:])) {
		t.Error("the file does not hold the token's hash")
	}
}

func TestFileModes(t *testing.T) {
	s, path := tempStore(t)
	if _, err := s.Allow("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v, want 0700", di.Mode().Perm())
	}
}

func TestRevoke(t *testing.T) {
	s, _ := tempStore(t)
	token, _ := s.Allow("apollo", "claude-code", KindPassthrough)
	if err := s.Revoke("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Check("apollo", "claude-code", KindPassthrough, token); ok {
		t.Error("Check is true after Revoke")
	}
	if list, err := s.List("apollo"); err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v; want empty", list, err)
	}
	if err := s.Revoke("apollo", "claude-code", KindPassthrough); err != nil {
		t.Errorf("second Revoke: %v", err)
	}
}

func TestCorruptFileIsReported(t *testing.T) {
	s, path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errCheck := s.Check("apollo", "claude-code", KindPassthrough, "t")
	_, errList := s.List("apollo")
	_, errAllow := s.Allow("apollo", "claude-code", KindPassthrough)
	for name, err := range map[string]error{"Check": errCheck, "List": errList, "Allow": errAllow} {
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s error = %v, want one naming %s", name, err, path)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "{" {
		t.Errorf("file now holds %q, want {", data)
	}
}

func TestConcurrentAllow(t *testing.T) {
	s, _ := tempStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Allow("apollo", fmt.Sprintf("agent-%02d", i), KindPassthrough); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := s.List("apollo")
	if err != nil || len(list) != 20 {
		t.Fatalf("List = %d grants, %v; want 20", len(list), err)
	}
}
