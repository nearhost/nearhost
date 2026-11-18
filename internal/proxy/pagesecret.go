package proxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nearhost/nearhost/internal/config"
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

// pageSecret returns the secret of Nearhost's page, creating it the first
// time. It is read from the file on each call, not kept, so every process
// uses what the file holds now. Its errors never hold the secret.
func pageSecret() (string, error) {
	path, err := config.PageSecretPath()
	if err != nil {
		return "", err
	}
	return readOrCreateSecret(path)
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
// bytes, or errBadSecret.
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

// hasPageCookie reports whether r carries the page cookie with the right
// value. Every cookie of that name counts, as a server on another port of
// localhost may have set one of its own.
func hasPageCookie(r *http.Request) bool {
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

// setPageCookie adds the page cookie to the response headers h. A secret
// that can't be read sets nothing.
func setPageCookie(h http.Header) {
	secret, err := pageSecret()
	if err != nil {
		return
	}
	h.Add("Set-Cookie", pageCookie+"="+secret+"; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=31536000")
}

// PageKeyURL returns the address that opens Nearhost's page at base (such
// as https://localhost:6327) and gives the browser the cookie that shows the
// document list: base/?k=<secret>. It is for opening in the browser only:
// printed or logged, it would give the secret away. Its errors never hold
// the secret.
func PageKeyURL(base string) (string, error) {
	secret, err := pageSecret()
	if err != nil {
		return "", err
	}
	return base + "/?" + url.Values{pageKeyParam: {secret}}.Encode(), nil
}
