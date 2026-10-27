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
	"time"
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

func TestSaveSyncs(t *testing.T) {
	s, path := tempStore(t)
	var synced []string
	old := syncFile
	syncFile = func(f *os.File) error {
		synced = append(synced, f.Name())
		if _, err := os.Stat(path); err == nil {
			t.Error("the file was renamed into place before the sync")
		}
		return f.Sync()
	}
	t.Cleanup(func() { syncFile = old })
	if _, err := s.Allow("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || filepath.Dir(synced[0]) != filepath.Dir(path) {
		t.Fatalf("synced %v, want one temp file beside %s", synced, path)
	}
}

func TestSaveThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "grants.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "grants.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := Open(link)
	token, err := s.Allow("apollo", "claude-code", KindPassthrough)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the path is no longer a symlink: %v, %v", info, err)
	}
	data, err := os.ReadFile(real)
	if err != nil || !strings.Contains(string(data), hash(token)) {
		t.Fatalf("the target does not hold the grant: %q, %v", data, err)
	}
	if ok, err := s.Check("apollo", "claude-code", KindPassthrough, token); err != nil || !ok {
		t.Fatalf("Check = %v, %v; want true", ok, err)
	}
}

func TestDanglingSymlinkIsAnError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing", "grants.json")
	link := filepath.Join(dir, "grants.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := Open(link).Allow("apollo", "claude-code", KindPassthrough)
	if err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("Allow error = %v, want one naming %s", err, link)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a file appeared at the target: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", info, err)
	}
}

func TestPrune(t *testing.T) {
	s, path := tempStore(t)
	for _, server := range []string{"apollo", "mini", "gone"} {
		if _, err := s.Allow(server, "claude-code", KindPassthrough); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Prune([]string{"apollo", "mini"})
	if err != nil || removed != 1 {
		t.Fatalf("Prune = %d, %v; want 1, nil", removed, err)
	}
	if got, _ := s.List("gone"); len(got) != 0 {
		t.Errorf("gone still has %v", got)
	}
	for _, server := range []string{"apollo", "mini"} {
		if got, _ := s.List(server); len(got) != 1 {
			t.Errorf("%s has %v, want one grant", server, got)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err = s.Prune([]string{"apollo", "mini"})
	if err != nil || removed != 0 {
		t.Fatalf("second Prune = %d, %v; want 0, nil", removed, err)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(old) || before.Size() != after.Size() {
		t.Errorf("the file was written by a Prune that removed nothing")
	}
}

func TestPruneMissingFile(t *testing.T) {
	s, path := tempStore(t)
	if removed, err := s.Prune([]string{"apollo"}); err != nil || removed != 0 {
		t.Fatalf("Prune = %d, %v; want 0, nil", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Prune created the file: %v", err)
	}
}

func TestValidate(t *testing.T) {
	s, path := tempStore(t)
	if err := s.Validate(); err != nil {
		t.Errorf("missing file: %v", err)
	}
	if _, err := s.Allow("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("valid file: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("corrupt file: error = %v, want one naming %s", err, path)
	}
}
