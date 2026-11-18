package proxy

import (
	"crypto/x509"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The bundle files and directories below are the ones crypto/x509 reads for
// its system pool on Linux, in its order.
var (
	systemCertFiles = []string{
		"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Gentoo
		"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL 6
		"/etc/ssl/ca-bundle.pem",                            // openSUSE
		"/etc/pki/tls/cacert.pem",                           // OpenELEC
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS, RHEL 7
		"/etc/ssl/cert.pem",                                 // Alpine
	}
	systemCertDirs = []string{
		"/etc/ssl/certs",     // SLES 10 and 11
		"/etc/pki/tls/certs", // Fedora, RHEL
	}
)

// systemRootPool is the hook IsTrusted uses to get the system roots. Tests
// replace it.
var systemRootPool = freshSystemRoots

// readCertFile reads one bundle or certificate file. Tests count its calls.
var readCertFile = os.ReadFile

// rootsCache is the last pool read from disk and the stat signature of the
// files it came from.
var rootsCache struct {
	sync.Mutex
	sig  string
	pool *x509.CertPool
}

// systemRoots returns the system roots read from disk now, and false when
// they cannot be read, in which case the caller falls back to the roots Go
// loaded at start.
func systemRoots() (*x509.CertPool, bool) {
	pool, err := systemRootPool()
	if err != nil || pool == nil {
		return nil, false
	}
	return pool, true
}

// freshSystemRoots reads the system roots the way crypto/x509 does for a new
// process: SSL_CERT_FILE replaces the bundle list, SSL_CERT_DIR replaces the
// directory list, the first bundle file that reads wins, and every file of
// every directory is added. Go loads its own pool once per process, so a CA
// trusted after start is missing from it. The parsed pool is kept until the
// path, size or modification time of one of the files changes, because the
// interface asks every second.
func freshSystemRoots() (*x509.CertPool, error) {
	files := systemCertFiles
	if f := os.Getenv("SSL_CERT_FILE"); f != "" {
		files = []string{f}
	}
	dirs := systemCertDirs
	if d := os.Getenv("SSL_CERT_DIR"); d != "" {
		dirs = filepath.SplitList(d)
	}

	// The bundle is the first file that can be read; stat it to know which.
	var bundle string
	var sig strings.Builder
	var firstErr error
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			if firstErr == nil && !errors.Is(err, fs.ErrNotExist) {
				firstErr = err
			}
			continue
		}
		bundle = f
		sig.WriteString(statSignature(f, info))
		break
	}
	var dirFiles []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			if firstErr == nil && !errors.Is(err, fs.ErrNotExist) {
				firstErr = err
			}
			continue
		}
		for _, e := range entries {
			if isSameDirSymlink(e, d) {
				continue
			}
			p := filepath.Join(d, e.Name())
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			dirFiles = append(dirFiles, p)
			sig.WriteString(statSignature(p, info))
		}
	}

	rootsCache.Lock()
	defer rootsCache.Unlock()
	if rootsCache.pool != nil && rootsCache.sig == sig.String() {
		return rootsCache.pool, nil
	}
	pool := x509.NewCertPool()
	found := false
	if bundle != "" {
		if data, err := readCertFile(bundle); err == nil {
			pool.AppendCertsFromPEM(data)
			found = true
		} else if firstErr == nil && !errors.Is(err, fs.ErrNotExist) {
			firstErr = err
		}
	}
	for _, p := range dirFiles {
		if data, err := readCertFile(p); err == nil {
			pool.AppendCertsFromPEM(data)
			found = true
		}
	}
	if !found && firstErr != nil {
		return nil, firstErr
	}
	rootsCache.sig, rootsCache.pool = sig.String(), pool
	return pool, nil
}

// statSignature is the part of a file's identity that changes when the system
// rewrites it.
func statSignature(path string, info fs.FileInfo) string {
	return path + "|" + strconv.FormatInt(info.Size(), 10) + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ";"
}

// isSameDirSymlink reports whether e is a symlink whose target has no slash,
// which crypto/x509 skips because the target is read on its own.
func isSameDirSymlink(e fs.DirEntry, dir string) bool {
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(filepath.Join(dir, e.Name()))
	return err == nil && !strings.ContainsRune(target, filepath.Separator)
}
