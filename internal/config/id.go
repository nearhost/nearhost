package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// idChars is how long an install id is.
const idChars = 6

// InstallID is this install's id: six lowercase base-32 characters, made on
// first use and kept in the config directory as id. It names the socket
// agents reach nearhost on, so two laptops serving one server account each
// get their own.
//
// The file also holds a fingerprint of the machine. A config directory
// copied to another machine (dotfiles, a migration) carries the id with it;
// the fingerprint no longer matches there, and a new id is made, so the two
// machines never share a socket.
func InstallID() (string, error) {
	path, err := sub("id")
	if err != nil {
		return "", err
	}
	return installIDFor(path, machineFingerprint)
}

// installIDFor is InstallID with the id file at path and the machine's
// fingerprint from fingerprint, "" when it cannot be read.
func installIDFor(path string, fingerprint func() string) (string, error) {
	id, stored, err := readIDFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return createID(path, fingerprint)
	}
	if err != nil {
		return "", err
	}
	fp := fingerprint()
	switch {
	case fp == "" || stored == fp:
		return id, nil
	case stored == "":
		// Written before ids had fingerprints: this machine keeps it.
		return id, writeID(path, id, fp)
	}
	id = newID()
	return id, writeID(path, id, fp)
}

// readIDFile reads the id and the fingerprint in the file at path. A file
// that is there but empty is being written by a process that started at
// the same moment: it is read again for a while.
func readIDFile(path string) (id, fingerprint string, err error) {
	for wait := 0; ; wait++ {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 && wait < 20 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if len(fields) == 0 || len(fields) > 2 || !validID(fields[0]) {
			return "", "", fmt.Errorf("%s does not hold an install id; remove it and nearhost makes a new one", path)
		}
		if len(fields) == 2 {
			fingerprint = fields[1]
		}
		return fields[0], fingerprint, nil
	}
}

// createID makes the id file. Two processes starting at once must end up
// with one id: the first to create the file wins, and the other reads what
// it wrote.
func createID(path string, fingerprint func() string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return installIDFor(path, fingerprint)
	}
	if err != nil {
		return "", err
	}
	id := newID()
	_, err = f.WriteString(idLine(id, fingerprint()))
	return id, errors.Join(err, f.Close())
}

// writeID replaces the id file, whole: a reader never sees half of it.
func writeID(path, id, fingerprint string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".id-*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(idLine(id, fingerprint))
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func idLine(id, fingerprint string) string {
	if fingerprint == "" {
		return id + "\n"
	}
	return id + " " + fingerprint + "\n"
}

func newID() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.EncodeToString(b))[:idChars]
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

// machineFingerprint is a short hash of what identifies this machine (see
// machineID). It is "" where nothing can be read.
func machineFingerprint() string {
	raw := machineID()
	if raw == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte("nearhost:"+raw)))[:16]
}

var platformUUIDLine = regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)

// platformUUID finds the IOPlatformUUID in ioreg's output.
func platformUUID(ioreg string) string {
	if m := platformUUIDLine.FindStringSubmatch(ioreg); m != nil {
		return m[1]
	}
	return ""
}
