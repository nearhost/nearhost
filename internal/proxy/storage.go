package proxy

import (
	"context"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"

	"github.com/nearhost/nearhost/internal/oserr"
)

// storageModule is the Caddy storage module name registered below.
const storageModule = "nearhost_file_system"

func init() {
	caddy.RegisterModule(Storage{})
}

// Storage is Caddy's file_system storage that waits for a file another
// handle holds instead of failing at once. On Windows a file cannot be
// removed or opened against an open handle that does not share that access,
// and Go shares no deletion: certmagic's lock files are read by every
// goroutine waiting for the lock, so an unlock that lands on such a read
// fails. The lock file then stays, and the goroutine that keeps it fresh,
// which stops only when the file is gone, keeps it fresh for good: no
// certificate for that name is issued again until Nearhost restarts. Each
// reload starts a new configuration that waits for the lock of every name
// whose certificate is still being issued, so starting and stopping projects
// in quick succession gets there. A certificate file read while it is being
// replaced fails the same way.
type Storage struct {
	Root string `json:"root,omitempty"`
}

// CaddyModule implements caddy.Module.
func (Storage) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.storage." + storageModule,
		New: func() caddy.Module { return new(Storage) },
	}
}

// CertMagicStorage implements caddy.StorageConverter.
func (s Storage) CertMagicStorage() (certmagic.Storage, error) {
	return &fileStorage{FileStorage: &certmagic.FileStorage{Path: s.Root}}, nil
}

var _ caddy.StorageConverter = (*Storage)(nil)

// fileStorage is certmagic's file storage with Unlock and Load waiting out
// another handle on the file.
type fileStorage struct {
	*certmagic.FileStorage
}

// inUseWait bounds how long a storage call waits for another handle to let
// go of its file. The handles in question are a lock file's read by a
// waiting goroutine or a certificate's replacement, which take moments.
const inUseWait = 5 * time.Second

// Unlock implements certmagic.Locker.
func (s *fileStorage) Unlock(ctx context.Context, name string) error {
	return whileInUse(func() error { return s.FileStorage.Unlock(ctx, name) })
}

// Load implements certmagic.Storage.
func (s *fileStorage) Load(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := whileInUse(func() error {
		var err error
		data, err = s.FileStorage.Load(ctx, key)
		return err
	})
	return data, err
}

// whileInUse calls f until it does not fail for a file another handle holds,
// or inUseWait has passed.
func whileInUse(f func() error) error {
	deadline := time.Now().Add(inUseWait)
	for {
		err := f()
		if !oserr.InUse(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
