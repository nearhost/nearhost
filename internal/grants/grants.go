// Package grants keeps the "Always allow" answers a person gave to coding
// agents. Only the SHA-256 of each grant's token is stored; the token itself
// goes to the agent once and is never kept on the laptop.
package grants

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// KindPassthrough is the kind of grant that lets an agent ask for a port to
// be forwarded without a prompt.
const KindPassthrough = "passthrough"

// KindDocuments is the kind of grant that lets an agent show documents in
// the laptop's browser without a prompt.
const KindDocuments = "documents"

// Grant is one "Always allow" answer. The token hash is kept in the file and
// is never returned, nor are the hashes of the roots a documents grant
// covers.
type Grant struct {
	Server, Requester, Kind string
	Created                 time.Time
}

// record is a grant as written to the file.
type record struct {
	Server    string    `json:"server"`
	Requester string    `json:"requester"`
	Kind      string    `json:"kind"`
	SHA256    string    `json:"sha256"`
	Created   time.Time `json:"created"`
	// Roots holds the rootHash of each folder a documents grant covers. A
	// file written before grants had roots has none, so its grants cover
	// no folder until one is added.
	Roots []string `json:"roots,omitempty"`
}

// fileData is the whole grants file.
type fileData struct {
	Grants []record `json:"grants"`
}

// Store is the grants file at one path. It is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store whose file is at path. The file and its directory
// are made by the first Allow.
func Open(path string) *Store { return &Store{path: path} }

// hash is the hex SHA-256 of token.
func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// rootHash is the hex SHA-256 of root on server, as the file keeps it, so
// the file does not name the folders it covers.
func rootHash(server, root string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + root))
	return hex.EncodeToString(sum[:])
}

// same reports whether r is the grant for server, requester and kind.
func (r record) same(server, requester, kind string) bool {
	return r.Server == server && r.Requester == requester && r.Kind == kind
}

// load reads the file. A missing file is an empty one; a file that cannot be
// read or parsed is an error naming the path, and is left as it is.
func (s *Store) load() (fileData, error) {
	var d fileData
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return fileData{}, fmt.Errorf("%s is not a grants file: %w", s.path, err)
	}
	return d, nil
}

// syncFile flushes f to disk. Tests replace it to see when it runs.
var syncFile = (*os.File).Sync

// syncDir flushes a directory's entries to disk. Tests replace it to see
// when it runs and to make it fail.
var syncDir = (*os.File).Sync

// target is the path a save writes to. When the grants file is a symlink it
// is the file the link points to, so the link survives the save. A link whose
// target is missing is an error: a save would otherwise make a new file the
// link was never meant to have.
func (s *Store) target() (string, error) {
	info, err := os.Lstat(s.path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return s.path, nil
	}
	resolved, err := filepath.EvalSymlinks(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s is a symlink to a file that does not exist", s.path)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", s.path, err)
	}
	return resolved, nil
}

// save writes d to a temporary file beside the grants file, syncs it, then
// renames it into place, so a reader never sees half of it and a crash does
// not leave an empty file. It then syncs the directory so the rename lasts,
// as far as it can: the file is in place by then, so that error is ignored.
func (s *Store) save(d fileData) error {
	path, err := s.target()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".grants-*")
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600, but the mode is set so it never depends on that.
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = syncFile(tmp)
	}
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if f, err := os.Open(dir); err == nil {
		syncDir(f)
		f.Close()
	}
	return nil
}

// Allow creates the grant for server, requester and kind, or replaces it,
// and returns its new token: 32 random bytes in unpadded base64url. The
// grant covers roots and no other: a replaced grant drops the roots it had.
func (s *Store) Allow(server, requester, kind string, roots ...string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return "", err
	}
	rec := record{Server: server, Requester: requester, Kind: kind, SHA256: hash(token), Created: time.Now().UTC()}
	for _, root := range roots {
		rec.cover(rootHash(server, root))
	}
	replaced := false
	for i, r := range d.Grants {
		if r.same(server, requester, kind) {
			d.Grants[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		d.Grants = append(d.Grants, rec)
	}
	if err := s.save(d); err != nil {
		return "", err
	}
	return token, nil
}

// cover adds the root hash h to r, unless r has it.
func (r *record) cover(h string) {
	if !r.covers(h) {
		r.Roots = append(r.Roots, h)
	}
}

// covers reports whether r holds the root hash h. It compares each in
// constant time and looks at all of them.
func (r record) covers(h string) bool {
	found := 0
	for _, have := range r.Roots {
		found |= subtle.ConstantTimeCompare([]byte(have), []byte(h))
	}
	return found == 1
}

// find is the index of the grant for server, requester and kind whose token
// is token, or -1. An empty token is never valid, and the token's hash is
// compared in constant time.
func (d fileData) find(server, requester, kind, token string) int {
	if token == "" {
		return -1
	}
	want := hash(token)
	for i, r := range d.Grants {
		if r.same(server, requester, kind) {
			if subtle.ConstantTimeCompare([]byte(r.SHA256), []byte(want)) == 1 {
				return i
			}
			return -1
		}
	}
	return -1
}

// Covers reports whether token is the current token of the grant for
// server, requester and kind, and whether that grant covers root.
func (s *Store) Covers(server, requester, kind, token, root string) (valid, covered bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return false, false, err
	}
	i := d.find(server, requester, kind, token)
	if i < 0 {
		return false, false, nil
	}
	return true, d.Grants[i].covers(rootHash(server, root)), nil
}

// Cover adds root to the grant for server, requester and kind when token is
// its current token, keeping the token, and reports whether it was. The file
// is not written when the grant covers root already.
func (s *Store) Cover(server, requester, kind, token, root string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return false, err
	}
	i := d.find(server, requester, kind, token)
	if i < 0 {
		return false, nil
	}
	h := rootHash(server, root)
	if d.Grants[i].covers(h) {
		return true, nil
	}
	d.Grants[i].cover(h)
	if err := s.save(d); err != nil {
		return false, err
	}
	return true, nil
}

// Check reports whether token is the current token of the grant for server,
// requester and kind. It compares in constant time, and an empty token is
// never valid.
func (s *Store) Check(server, requester, kind, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return false, err
	}
	return d.find(server, requester, kind, token) >= 0, nil
}

// List returns the grants for server, sorted by requester. A missing file is
// an empty list.
func (s *Store) List(server string) ([]Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Grant
	for _, r := range d.Grants {
		if r.Server == server {
			out = append(out, Grant{Server: r.Server, Requester: r.Requester, Kind: r.Kind, Created: r.Created})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requester != out[j].Requester {
			return out[i].Requester < out[j].Requester
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// Revoke removes the grant for server, requester and kind. A grant that is
// not there is not an error.
func (s *Store) Revoke(server, requester, kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	kept := d.Grants[:0]
	for _, r := range d.Grants {
		if !r.same(server, requester, kind) {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(d.Grants) {
		return nil
	}
	d.Grants = kept
	return s.save(d)
}

// Prune removes every grant whose server is not in keep and returns how many
// it removed. The file is not written when nothing is removed, and a missing
// file counts as none.
func (s *Store) Prune(keep []string) (removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(keep))
	for _, server := range keep {
		known[server] = true
	}
	kept := d.Grants[:0]
	for _, r := range d.Grants {
		if known[r.Server] {
			kept = append(kept, r)
		}
	}
	removed = len(d.Grants) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	d.Grants = kept
	if err := s.save(d); err != nil {
		return 0, err
	}
	return removed, nil
}

// Validate loads the file and returns the error if it cannot be read or
// parsed, or if it is a symlink to a file that does not exist, which a save
// would refuse. A missing file is valid.
func (s *Store) Validate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.target(); err != nil {
		return err
	}
	_, err := s.load()
	return err
}
