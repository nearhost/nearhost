package grants

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
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
	testutil.WantPerm(t, path, 0o600)
	testutil.WantPerm(t, filepath.Dir(path), 0o700)
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
	testutil.Symlink(t, real, link)
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
	testutil.Symlink(t, target, link)
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

func TestValidateDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "grants.json")
	testutil.Symlink(t, filepath.Join(dir, "missing", "grants.json"), link)
	if err := Open(link).Validate(); err == nil || !strings.Contains(err.Error(), link) {
		t.Errorf("Validate error = %v, want one naming %s", err, link)
	}
}

func TestSaveSyncsDirectoryBestEffort(t *testing.T) {
	s, path := tempStore(t)
	var synced []string
	old := syncDir
	syncDir = func(f *os.File) error {
		synced = append(synced, f.Name())
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the directory was synced before the rename: %v", err)
		}
		return errors.New("sync not supported")
	}
	t.Cleanup(func() { syncDir = old })
	if _, err := s.Allow("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatalf("Allow = %v; a failed directory sync must not fail the save", err)
	}
	if len(synced) != 1 || synced[0] != filepath.Dir(path) {
		t.Fatalf("synced %v, want the directory %s", synced, filepath.Dir(path))
	}
}

// rootHashOf is the hash the file keeps for root on server.
func rootHashOf(server, root string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + root))
	return hex.EncodeToString(sum[:])
}

func TestRootsCoverOnlyThoseApproved(t *testing.T) {
	s, path := tempStore(t)
	token, err := s.Allow("apollo", "claude-code", KindDocuments, "/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, server, token, root string
		valid, covered            bool
	}{
		{"approved root", "apollo", token, "/srv/app", true, true},
		{"new root", "apollo", token, "/srv/other", true, false},
		{"a folder below the root", "apollo", token, "/srv/app/docs", true, false},
		{"other token", "apollo", "!" + token[1:], "/srv/app", false, false},
		{"empty token", "apollo", "", "/srv/app", false, false},
		{"other server", "mini", token, "/srv/app", false, false},
	} {
		valid, covered, err := s.Covers(c.server, "claude-code", KindDocuments, c.token, c.root)
		if err != nil || valid != c.valid || covered != c.covered {
			t.Errorf("%s: Covers = %v, %v, %v; want %v, %v", c.name, valid, covered, err, c.valid, c.covered)
		}
	}

	// Cover adds a root and keeps the token.
	if ok, err := s.Cover("apollo", "claude-code", KindDocuments, token, "/srv/other"); err != nil || !ok {
		t.Fatalf("Cover = %v, %v", ok, err)
	}
	if valid, covered, _ := s.Covers("apollo", "claude-code", KindDocuments, token, "/srv/other"); !valid || !covered {
		t.Errorf("after Cover: %v, %v", valid, covered)
	}
	// Twice is once.
	if ok, err := s.Cover("apollo", "claude-code", KindDocuments, token, "/srv/other"); err != nil || !ok {
		t.Fatalf("Cover again = %v, %v", ok, err)
	}
	// A wrong token adds nothing.
	if ok, err := s.Cover("apollo", "claude-code", KindDocuments, "!"+token[1:], "/srv/third"); err != nil || ok {
		t.Fatalf("Cover with a wrong token = %v, %v", ok, err)
	}
	if _, covered, _ := s.Covers("apollo", "claude-code", KindDocuments, token, "/srv/third"); covered {
		t.Error("a wrong token added a root")
	}

	// The file holds each root's hash, once, and never the root.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/srv/") {
		t.Fatalf("the file holds a root:\n%s", data)
	}
	for _, root := range []string{"/srv/app", "/srv/other"} {
		if n := strings.Count(string(data), rootHashOf("apollo", root)); n != 1 {
			t.Errorf("%s's hash is in the file %d times:\n%s", root, n, data)
		}
	}

	// A new token covers only the roots it was given.
	again, err := s.Allow("apollo", "claude-code", KindDocuments, "/srv/third")
	if err != nil {
		t.Fatal(err)
	}
	if _, covered, _ := s.Covers("apollo", "claude-code", KindDocuments, again, "/srv/app"); covered {
		t.Error("a new token kept the old roots")
	}
	if _, covered, _ := s.Covers("apollo", "claude-code", KindDocuments, again, "/srv/third"); !covered {
		t.Error("a new token does not cover its root")
	}
}

// A root approved in one run is covered in the next, which reads the file
// afresh.
func TestRootsSurviveAReload(t *testing.T) {
	s, path := tempStore(t)
	token, err := s.Allow("apollo", "claude-code", KindDocuments, "/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Cover("apollo", "claude-code", KindDocuments, token, "/srv/other"); err != nil || !ok {
		t.Fatalf("Cover = %v, %v", ok, err)
	}
	next := Open(path)
	for _, root := range []string{"/srv/app", "/srv/other"} {
		if valid, covered, err := next.Covers("apollo", "claude-code", KindDocuments, token, root); err != nil || !valid || !covered {
			t.Errorf("%s after a reload: %v, %v, %v", root, valid, covered, err)
		}
	}
	if _, covered, _ := next.Covers("apollo", "claude-code", KindDocuments, token, "/srv/new"); covered {
		t.Error("a root never approved is covered after a reload")
	}
}

// A file written before grants had roots loads, each grant with none: its
// token is still good, and every root asks once.
func TestOldFileHasNoRoots(t *testing.T) {
	s, path := tempStore(t)
	token := "old-token"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf(`{"grants": [{"server": "apollo", "requester": "claude-code", "kind": "documents", "sha256": %q, "created": "2026-10-28T12:00:00Z"}]}`, hash(token))
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	valid, covered, err := s.Covers("apollo", "claude-code", KindDocuments, token, "/srv/app")
	if err != nil || !valid || covered {
		t.Fatalf("Covers = %v, %v, %v; want a valid token that covers nothing", valid, covered, err)
	}
	if ok, err := s.Cover("apollo", "claude-code", KindDocuments, token, "/srv/app"); err != nil || !ok {
		t.Fatalf("Cover = %v, %v", ok, err)
	}
	if valid, covered, _ := s.Covers("apollo", "claude-code", KindDocuments, token, "/srv/app"); !valid || !covered {
		t.Errorf("after Cover: %v, %v", valid, covered)
	}
}

// A passthrough grant is given no roots, and its file entry has none.
func TestPassthroughHasNoRoots(t *testing.T) {
	s, path := tempStore(t)
	if _, err := s.Allow("apollo", "claude-code", KindPassthrough); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "roots") {
		t.Fatalf("a passthrough grant has roots:\n%s", data)
	}
}
