package proxy

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// localIssuer is the storage key of the local CA's issuer. certmagic keeps
// each certificate it issues under certificates/local/<name>/, as
// <name>.crt (the chain, leaf first), <name>.key and <name>.json. The
// proxy's config names no other issuer, so every certificate there is one
// the local CA can issue again on the next handshake.
const localIssuer = "local"

// expiredCertMargin is how long a certificate must have been expired before
// pruneExpiredCertificates removes it, so that one Caddy would renew at
// once, or one a clock set back a little shows as just expired, stays.
const expiredCertMargin = 24 * time.Hour

// maxCertFile bounds how much of a certificate file is read: a chain is a
// few kilobytes.
const maxCertFile = 1 << 20

// pruneExpiredCertificates removes from storageRoot each certificate of the
// local CA that expired more than expiredCertMargin before now, with its key
// and metadata. Caddy renews only the certificates of names it still
// serves, so without this one for each name a pattern host was asked for
// stays on disk for good. A clock far ahead makes every certificate look
// expired and removes them all; the local CA issues each again, as the
// config loads or as its name is next asked for, so the cost is the issuing.
//
// Only a directory under certificates/local that holds nothing but files
// named for it, a .crt among them, is removed: the CA under pki, the lock
// files, other issuers' certificates and anything unexpected stay. Nothing
// is reached through a symlink. Problems are logged and never fatal.
//
// Call it with caddyMu held while Caddy is not running, before the config
// that starts it is loaded, so Caddy never meets a directory being removed.
func pruneExpiredCertificates(storageRoot string, now time.Time) {
	root, err := os.OpenRoot(storageRoot)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			emit("", "Couldn't check the proxy's storage for expired certificates: "+err.Error())
		}
		return
	}
	defer root.Close()
	issuerDir := filepath.Join("certificates", localIssuer)
	for _, dir := range []string{"certificates", issuerDir} {
		info, err := root.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		switch {
		case err != nil:
		case info.Mode()&fs.ModeSymlink != 0:
			err = errors.New("it is a symlink")
		case !info.IsDir():
			err = errors.New("it is not a folder")
		}
		if err != nil {
			emit("", fmt.Sprintf("Couldn't check %s for expired certificates: %v", filepath.Join(storageRoot, dir), err))
			return
		}
	}
	entries, err := readDir(root, issuerDir)
	if err != nil {
		emit("", fmt.Sprintf("Couldn't check %s for expired certificates: %v", filepath.Join(storageRoot, issuerDir), err))
		return
	}

	var removed, skipped, failed int
	var skipName, failName string
	var skipErr, failErr error
	for _, e := range entries {
		switch e.Type() & fs.ModeType {
		case fs.ModeDir:
		case fs.ModeSymlink:
			skipped++
			if skipName == "" {
				skipName, skipErr = e.Name(), errors.New("it is a symlink")
			}
			continue
		default:
			continue
		}
		dir := filepath.Join(issuerDir, e.Name())
		files, notAfter, err := certDir(root, dir, e.Name())
		if err != nil {
			skipped++
			if skipName == "" {
				skipName, skipErr = e.Name(), err
			}
			continue
		}
		if now.Sub(notAfter) <= expiredCertMargin {
			continue
		}
		if err := removeCertDir(root, dir, files); err != nil {
			failed++
			if failName == "" {
				failName, failErr = e.Name(), err
			}
			continue
		}
		removed++
	}

	if removed > 0 {
		emit("", fmt.Sprintf("Removed %s from the proxy's storage.", count(removed, "expired certificate")))
	}
	if skipped > 0 {
		verb := "don't"
		if skipped == 1 {
			verb = "doesn't"
		}
		emit("", fmt.Sprintf("Left %s in %s alone that %s hold just a certificate and its key, such as %s: %v.",
			count(skipped, "certificate folder"), filepath.Join(storageRoot, issuerDir), verb, skipName, skipErr))
	}
	if failed > 0 {
		emit("", fmt.Sprintf("Couldn't remove %s from the proxy's storage, such as %s: %v.",
			count(failed, "expired certificate"), failName, failErr))
	}
}

// count is n and noun, plural unless n is 1.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// readDir lists the directory name in root.
func readDir(root *os.Root, name string) ([]fs.DirEntry, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// certDir checks that dir, the directory of the certificate for name, holds
// only regular files named name.crt, name.key and name.json, name.crt among
// them, and returns those files and when the leaf, the first certificate in
// name.crt, expires.
func certDir(root *os.Root, dir, name string) (files []string, notAfter time.Time, err error) {
	entries, err := readDir(root, dir)
	if err != nil {
		return nil, time.Time{}, err
	}
	crt := name + ".crt"
	hasCrt := false
	for _, e := range entries {
		switch e.Name() {
		case crt, name + ".key", name + ".json":
		default:
			return nil, time.Time{}, fmt.Errorf("it holds %s", e.Name())
		}
		if !e.Type().IsRegular() {
			return nil, time.Time{}, fmt.Errorf("its %s is not a file", e.Name())
		}
		hasCrt = hasCrt || e.Name() == crt
		files = append(files, e.Name())
	}
	if !hasCrt {
		return nil, time.Time{}, fmt.Errorf("it has no %s", crt)
	}
	f, err := root.Open(filepath.Join(dir, crt))
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCertFile))
	f.Close()
	if err != nil {
		return nil, time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, time.Time{}, fmt.Errorf("its %s holds no certificate", crt)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("its %s: %w", crt, err)
	}
	return files, leaf.NotAfter, nil
}

// removeCertDir removes files from dir, the .crt last, and then dir. A file
// that cannot be removed, such as one another program holds open on Windows,
// stops it there, and the .crt it keeps until last lets the next start
// find the rest and try again.
func removeCertDir(root *os.Root, dir string, files []string) error {
	var crt string
	for _, f := range files {
		if strings.HasSuffix(f, ".crt") {
			crt = f
			continue
		}
		if err := root.Remove(filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	if err := root.Remove(filepath.Join(dir, crt)); err != nil {
		return err
	}
	return root.Remove(dir)
}
