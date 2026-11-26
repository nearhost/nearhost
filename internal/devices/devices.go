// Package devices holds the devices paired with a networked core: their
// token hashes on disk, the short-lived pair codes and auth tokens in
// memory, the HMAC site cookies, and the limit on failed attempts.
package devices

import (
	"crypto/hmac"
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
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/config"
)

const (
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength   = 8
	codeLife     = 10 * time.Minute
	authLife     = 60 * time.Second
	touchEvery   = time.Minute
	failWindow   = 10 * time.Minute
	failLimit    = 10
)

var (
	// ErrCodeInvalid is a pair code that is unknown, used or expired.
	ErrCodeInvalid = errors.New("that pair code is not valid")
	// ErrNoDevice is a device name that is not paired.
	ErrNoDevice = errors.New("no such device")
)

// Device is one paired device. Hash is the SHA-256 of its token; the token
// itself is never stored.
type Device struct {
	Name     string    `json:"name"`
	ID       string    `json:"id"`
	Hash     string    `json:"hash"`
	Paired   time.Time `json:"paired"`
	LastSeen time.Time `json:"last_seen"`
	LastAddr string    `json:"last_addr"`
}

// PairCode is a code a device types to pair.
type PairCode struct {
	Code    string
	Expires time.Time
}

type fileData struct {
	Devices []Device `json:"devices"`
}

type codeState struct {
	expires time.Time
	device  string // the name given, once paired
	paired  bool
}

type authState struct {
	device  string
	host    string
	expires time.Time
}

// Store is the devices file at one path plus the in-memory codes, tokens and
// failure counts. It is safe for concurrent use.
type Store struct {
	path string
	key  []byte
	now  func() time.Time

	mu      sync.Mutex
	devices []Device
	mtime   time.Time // of the file as last read or written
	size    int64
	loaded  bool
	loadErr error
	codes   map[string]*codeState
	auths   map[string]authState
	fails   map[string][]time.Time
}

// Open returns the store whose file is at path. key signs site cookies. The
// file and its directory are made by the first pairing.
func Open(path string, key []byte) *Store {
	return &Store{
		path:  path,
		key:   key,
		now:   time.Now,
		codes: map[string]*codeState{},
		auths: map[string]authState{},
		fails: map[string][]time.Time{},
	}
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("devices: no randomness: %v", err))
	}
	return b
}

// refresh re-reads the file when its mtime or size differ from the last read
// or write, so a change by another process is seen. The caller holds s.mu.
func (s *Store) refresh() error {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.devices, s.loaded, s.loadErr = nil, true, nil
		s.mtime, s.size = time.Time{}, 0
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if s.loaded && s.loadErr == nil && info.ModTime().Equal(s.mtime) && info.Size() == s.size {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var d fileData
	if err := json.Unmarshal(data, &d); err != nil {
		s.loaded, s.loadErr = true, fmt.Errorf("%s is not a devices file: %w", s.path, err)
		s.mtime, s.size = info.ModTime(), info.Size()
		return s.loadErr
	}
	s.devices, s.loaded, s.loadErr = d.Devices, true, nil
	s.mtime, s.size = info.ModTime(), info.Size()
	return nil
}

// save writes the device list and records the file's new stamp. The caller
// holds s.mu.
func (s *Store) save() error {
	data, err := json.MarshalIndent(fileData{Devices: s.devices}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.mtime, s.size = info.ModTime(), info.Size()
	}
	return nil
}

// NewPairCode makes a code that pairs one device within ten minutes.
func (s *Store) NewPairCode() PairCode {
	b := randomBytes(codeLength)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])&31] // 32 symbols, so no bias
	}
	code := string(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for c, st := range s.codes {
		if now.After(st.expires.Add(codeLife)) {
			delete(s.codes, c)
		}
	}
	exp := now.Add(codeLife)
	s.codes[code] = &codeState{expires: exp}
	return PairCode{Code: code, Expires: exp}
}

// PairStatus reports whether code has been used, and by which device name,
// or has expired unused.
func (s *Store) PairStatus(code string) (device string, paired bool, expired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.codes[normalise(code)]
	if !ok {
		return "", false, false
	}
	if st.paired {
		return st.device, true, false
	}
	return "", false, s.now().After(st.expires)
}

func normalise(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// Pair spends code and adds a device called name, deduplicated as "iPad",
// "iPad 2". It returns the device token, which is shown once. A failure
// counts against addr.
func (s *Store) Pair(code, name, addr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	st, ok := s.codes[normalise(code)]
	if !ok || st.paired || now.After(st.expires) {
		s.failLocked(addr)
		return "", ErrCodeInvalid
	}
	if err := s.refresh(); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Device"
	}
	final := name
	for n := 2; s.hasName(final); n++ {
		final = fmt.Sprintf("%s %d", name, n)
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	s.devices = append(s.devices, Device{
		Name:     final,
		ID:       hex.EncodeToString(randomBytes(16)),
		Hash:     hash(token),
		Paired:   now,
		LastSeen: now,
		LastAddr: addr,
	})
	if err := s.save(); err != nil {
		s.devices = s.devices[:len(s.devices)-1]
		return "", err
	}
	st.paired, st.device = true, final
	return token, nil
}

func (s *Store) hasName(name string) bool {
	for _, d := range s.devices {
		if d.Name == name {
			return true
		}
	}
	return false
}

// Device returns the device that owns token. It re-reads the file when it
// changed, and records LastSeen at most once a minute.
func (s *Store) Device(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	h := hash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh() != nil {
		return Device{}, false
	}
	for i := range s.devices {
		d := &s.devices[i]
		if subtle.ConstantTimeCompare([]byte(d.Hash), []byte(h)) != 1 {
			continue
		}
		if now := s.now(); now.Sub(d.LastSeen) >= touchEvery {
			d.LastSeen = now
			_ = s.save() // best effort: LastSeen is a convenience
		}
		return *d, true
	}
	return Device{}, false
}

// List returns the paired devices.
func (s *Store) List() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refresh(); err != nil {
		return nil, err
	}
	return append([]Device(nil), s.devices...), nil
}

// Revoke removes the device called name.
func (s *Store) Revoke(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refresh(); err != nil {
		return err
	}
	for i, d := range s.devices {
		if d.Name == name {
			old := s.devices
			s.devices = append(append([]Device(nil), old[:i]...), old[i+1:]...)
			if err := s.save(); err != nil {
				s.devices = old
				return err
			}
			return nil
		}
	}
	return ErrNoDevice
}

// NewAuthToken makes a token that signs deviceID in on host once, within
// sixty seconds.
func (s *Store) NewAuthToken(deviceID, host string) string {
	tok := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for t, a := range s.auths {
		if now.After(a.expires) {
			delete(s.auths, t)
		}
	}
	s.auths[tok] = authState{device: deviceID, host: strings.ToLower(host), expires: now.Add(authLife)}
	return tok
}

// RedeemAuth spends token for host and returns the device it was made for. A
// token presented for another host is refused and kept, so the right host can
// still use it.
func (s *Store) RedeemAuth(token, host string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.auths[token]
	if !ok || a.host != strings.ToLower(host) {
		return "", false
	}
	delete(s.auths, token)
	if s.now().After(a.expires) {
		return "", false
	}
	if s.refresh() != nil || !s.hasID(a.device) {
		return "", false
	}
	return a.device, true
}

func (s *Store) hasID(id string) bool {
	for _, d := range s.devices {
		if d.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) mac(deviceID, host string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(deviceID + "|" + strings.ToLower(host)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SiteCookie is the cookie value for deviceID on host.
func (s *Store) SiteCookie(deviceID, host string) string {
	return deviceID + "." + s.mac(deviceID, host)
}

// CheckSite verifies a site cookie for host and that its device is still
// paired, so a revoked device's cookies stop working at once.
func (s *Store) CheckSite(value, host string) (string, bool) {
	id, sig, ok := strings.Cut(value, ".")
	if !ok || id == "" || !hmac.Equal([]byte(sig), []byte(s.mac(id, host))) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh() != nil || !s.hasID(id) {
		return "", false
	}
	return id, true
}

// Allow reports whether addr may try again: false after ten failed Pair or
// RedeemAuth calls within ten minutes.
func (s *Store) Allow(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recent(addr)) < failLimit
}

// Fail counts a failed attempt from addr. Pair does this itself; callers
// count a refused RedeemAuth.
func (s *Store) Fail(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLocked(addr)
}

func (s *Store) failLocked(addr string) {
	s.fails[addr] = append(s.recent(addr), s.now())
}

// recent drops failures older than the window and returns the rest.
func (s *Store) recent(addr string) []time.Time {
	cut := s.now().Add(-failWindow)
	var keep []time.Time
	for _, t := range s.fails[addr] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(s.fails, addr)
	} else {
		s.fails[addr] = keep
	}
	return keep
}
