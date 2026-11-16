package proxy

import (
	"context"
	"path/filepath"
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
//
// An unlock that succeeds still leaves a moment in which the lock file is
// pending deletion: DeleteFile marks the file and then closes its handle, and
// in between the file can be neither opened nor created (ACCESS_DENIED).
// A goroutine waiting for the lock reads the file, and one taking it creates
// it, so either can land there, and certmagic's lock acquisition then fails
// outright, outside the retries of an issuance: that name had no certificate
// until the next reload. So does certmagic's removal of a stale lock file
// when a waiter's read refuses it. Lock tries again instead (see
// lockSettling).
//
// Store replaces a file by renaming a temporary one onto it, and on Windows
// that rename is refused (ACCESS_DENIED) while a reader has the file open;
// that only fails the attempt, which certmagic retries after a minute.
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

// fileStorage is certmagic's file storage with Lock, Unlock and Load
// waiting out another handle on the file.
type fileStorage struct {
	*certmagic.FileStorage
}

// inUseWait bounds how long a storage call waits for another handle to let
// go of its file. The handles in question are a lock file's read by a
// waiting goroutine or a certificate's replacement, which take moments.
const inUseWait = 5 * time.Second

// Lock implements certmagic.Locker. When certmagic's attempt fails while the
// lock file may just be finishing its removal (see lockSettling), it tries
// again, for up to inUseWait or until ctx ends.
func (s *fileStorage) Lock(ctx context.Context, name string) error {
	deadline := time.Now().Add(inUseWait)
	file := filepath.Join(s.Path, "locks", certmagic.StorageKeys.Safe(name)+".lock")
	for {
		err := s.FileStorage.Lock(ctx, name)
		if err == nil || ctx.Err() != nil || time.Now().After(deadline) || !lockSettling(file) {
			return err
		}
		lockWaits()
		select {
		case <-ctx.Done():
			return err
		case <-time.After(10 * time.Millisecond):
		}
	}
}

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

// lockWaits is called each time Lock waits for a lock file's removal to
// finish; tests watch it.
var lockWaits = func() {}

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
