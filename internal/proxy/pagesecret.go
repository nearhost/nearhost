package proxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
)

// pageCookie is the cookie that lets a browser see the documents listed on
// Nearhost's page. The __Host- prefix makes browsers refuse it unless it is
// Secure, has Path=/ and names no Domain, so no other host can set it.
const pageCookie = "__Host-nearhost"

// pageKeyParam is the query parameter of /?k=<secret>, the address Nearhost
// opens its page at so the browser gets the cookie.
const pageKeyParam = "k"

// pageSecretBytes is how many random bytes the secret holds; the file holds
// them as hex.
const pageSecretBytes = 32

// pageSecretAttempts bounds the rounds pageSecret takes to read, create or
// replace the file, should other processes keep changing it meanwhile.
const pageSecretAttempts = 5

// errBadSecret is a secret file that does not hold a secret: left short or
// empty by a crash, or edited by hand.
var errBadSecret = errors.New("the page secret file is malformed")

// chmod is os.Chmod, replaced in tests to fail.
var chmod = os.Chmod

// secretErrLogged is set once a failure to read or create the secret has
// been logged, so the log says so once per process.
var secretErrLogged atomic.Bool

// pageSecret returns the secret of Nearhost's page, creating it the first
// time. It is read from the file on each call, not kept, so every process
// uses what the file holds now. Its errors never hold the secret.
func pageSecret() (string, error) {
	path, err := config.PageSecretPath()
	if err == nil {
		var secret string
		if secret, err = readOrCreateSecret(path); err == nil {
			return secret, nil
		}
	}
	// The error names paths only, never the secret.
	if secretErrLogged.CompareAndSwap(false, true) {
		emit("", "Nearhost's page can't list documents: its secret can't be read or created: "+err.Error())
	}
	return "", err
}

// readOrCreateSecret returns the secret in the file at path. A missing file
// is created with a new secret, and a malformed one replaced with one. Two
// processes creating it at once end up with the same secret: each writes
// its own to a temporary file and links that into place, which fails rather
// than overwrite, and the one that lost reads the winner's.
func readOrCreateSecret(path string) (string, error) {
	for range pageSecretAttempts {
		secret, err := readSecret(path)
		switch {
		case err == nil:
			return secret, nil
		case errors.Is(err, fs.ErrNotExist):
			if err := createSecret(path); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
		case errors.Is(err, errBadSecret):
			if err := replaceSecret(path); err != nil {
				return "", err
			}
		default:
			return "", err
		}
	}
	return "", errors.New("could not settle the secret of Nearhost's page in " + path)
}

// readSecret reads the secret at path: exactly the hex of pageSecretBytes
// bytes, or errBadSecret. Outside Windows it narrows a wider mode to 0600,
// and a file it can't narrow is errBadSecret too, so it gets replaced.
func readSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) != 2*pageSecretBytes {
		return "", errBadSecret
	}
	if _, err := hex.DecodeString(string(data)); err != nil {
		return "", errBadSecret
	}
	// A file restored from a backup may have come back readable by others.
	// On Windows the mode means nothing: the folder's access list counts.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm()&^0o600 != 0 {
			if err := chmod(path, 0o600); err != nil {
				return "", errBadSecret
			}
		}
	}
	return string(data), nil
}

// newSecretFile writes a new secret to a temporary file beside path, mode
// 0600 (os.CreateTemp creates it so, with O_EXCL), flushed to disk, and
// returns the file's name.
func newSecretFile(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	removeStaleTemps(dir)
	b := make([]byte, pageSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".page-secret-")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(hex.EncodeToString(b))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// staleTemp is how old a temporary secret file must be before it counts as
// left by a crash: one still being written belongs to another process.
const staleTemp = time.Minute

// removeStaleTemps removes the temporary secret files in dir older than
// staleTemp. It does its best and reports nothing.
func removeStaleTemps(dir string) {
	names, _ := filepath.Glob(filepath.Join(dir, ".page-secret-*"))
	for _, name := range names {
		if info, err := os.Lstat(name); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > staleTemp {
			os.Remove(name)
		}
	}
}

// createSecret puts a new secret at path unless a file is there already,
// which it reports as fs.ErrExist. The file appears whole or not at all. On
// a file system without hard links it creates path with O_EXCL and writes
// to it; a crash then can leave it short, which readOrCreateSecret replaces.
func createSecret(path string) error {
	tmp, err := newSecretFile(path)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	err = os.Link(tmp, path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	data, rerr := os.ReadFile(tmp)
	if rerr != nil {
		return rerr
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// replaceSecret puts a new secret at path in one rename, over the
// malformed file there.
func replaceSecret(path string) error {
	tmp, err := newSecretFile(path)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// secretMatches reports whether got is secret, in time that depends on
// neither value's content. A wrong length returns at once, which tells only
// the length, and every secret has the same one.
func secretMatches(got, secret string) bool {
	return secret != "" && subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// atPageHost reports whether r is for Nearhost's page at its own host,
// hostname.PageHost, with the port stripped and case ignored. The page
// cookie is given and honoured there alone: a cookie is sent to every port
// of the host it was set at, and at localhost that would hand it to any dev
// server the user browses there. Cookies ignore ports at nearhost.localhost
// too, so a server the user opens there on another port would receive the
// cookie; SameSite=Strict keeps cross-site navigations from carrying it.
func atPageHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, hostname.PageHost)
}

// hasPageCookie reports whether r, a request at hostname.PageHost, carries
// the page cookie with the right value. A request at any other host has no
// page cookie, whatever it sends: one there could only have been set by
// another server on that host. Every cookie of that name counts, as another
// server on a port of hostname.PageHost may have set one of its own.
func hasPageCookie(r *http.Request) bool {
	if !atPageHost(r) {
		return false
	}
	cookies := r.CookiesNamed(pageCookie)
	if len(cookies) == 0 {
		return false
	}
	secret, err := pageSecret()
	if err != nil {
		return false
	}
	ok := false
	for _, c := range cookies {
		if secretMatches(c.Value, secret) {
			ok = true
		}
	}
	return ok
}

// setPageCookie adds the page cookie to the response headers h for the
// request r. It sets nothing for a request at a host other than
// hostname.PageHost, or when the secret can't be read.
func setPageCookie(h http.Header, r *http.Request) {
	if !atPageHost(r) {
		return
	}
	secret, err := pageSecret()
	if err != nil {
		return
	}
	h.Add("Set-Cookie", pageCookie+"="+secret+"; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=31536000")
}

// PageKeyURL returns the address that opens Nearhost's page at base (such
// as https://nearhost.localhost:6327) and gives the browser the cookie that
// shows the document list: base/?k=<secret>. It is for opening in the
// browser only: printed or logged, it would give the secret away. Its
// errors never hold the secret.
func PageKeyURL(base string) (string, error) {
	secret, err := pageSecret()
	if err != nil {
		return "", err
	}
	return base + "/?" + url.Values{pageKeyParam: {secret}}.Encode(), nil
}
