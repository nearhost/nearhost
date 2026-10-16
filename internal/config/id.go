package config

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// idChars is how long an install id is.
const idChars = 6

// InstallID is this install's id: six lowercase base-32 characters, made on
// first use and kept in the config directory as id. It names the socket
// agents reach nearhost on, so two laptops serving one server account each
// get their own.
func InstallID() (string, error) {
	path, err := sub("id")
	if err != nil {
		return "", err
	}
	return installID(path)
}

func installID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if validID(id) {
			return id, nil
		}
		return "", fmt.Errorf("%s does not hold an install id; remove it and nearhost makes a new one", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 5)
	rand.Read(b)
	id := strings.ToLower(base32.StdEncoding.EncodeToString(b))[:idChars]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	// Two processes starting at once must end up with one id: the first to
	// create the file wins, and the other reads what it wrote.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return installID(path)
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(id + "\n")
	return id, errors.Join(err, f.Close())
}

func validID(id string) bool {
	if len(id) != idChars {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}
