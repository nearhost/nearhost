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
// again, pausing for up to inUseWait in all or until ctx ends.
//
// The retry is deliberately broad: it goes on while the lock file is pending
// deletion, gone, readable again, or held by another handle, since certmagic
// reports none of these apart. At the deadline the last attempt's own error
// is returned. An ACCESS_DENIED that lasts, from a file the process has no
// right to, is none of these, and fails at once.
func (s *fileStorage) Lock(ctx context.Context, name string) error {
	lockStarts()
	file := filepath.Join(s.Path, "locks", certmagic.StorageKeys.Safe(name)+".lock")
	return retryLock(ctx, lockRetryWindow,
		func() error { return s.FileStorage.Lock(ctx, name) },
		func() bool { return lockSettling(file) })
}

// lockRetryWindow is how long Lock goes on trying again; tests shorten it.
var lockRetryWindow = inUseWait

// lockRetryPause is the pause between attempts at a lock.
const lockRetryPause = 10 * time.Millisecond

// retryLock calls lock until it succeeds, ctx ends, or it fails while
// settling reports false or it has paused for window in all. Only the pauses
// count towards window, not the time inside an attempt: certmagic's Lock can
// wait many seconds for a holder before it fails on that holder's unlock, at
// the first attempt or at a later one, and that waiter must still get its
// retries.
func retryLock(ctx context.Context, window time.Duration, lock func() error, settling func() bool) error {
	var paused time.Duration
	for {
		err := lock()
		if err == nil || ctx.Err() != nil || !settling() || paused >= window {
			return err
		}
		lockWaits()
		select {
		case <-ctx.Done():
			return err
		case <-time.After(lockRetryPause):
			paused += lockRetryPause
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

// lockStarts is called as Lock starts; tests watch it.
var lockStarts = func() {}

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
