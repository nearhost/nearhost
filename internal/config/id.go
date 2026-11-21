package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/fsretry"
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
	if errors.Is(err, errEmptyID) {
		return replaceEmptyID(path, fingerprint)
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

// idWait is how long a file with no id in it may be a writer's work in
// progress. An empty file older than this is a leftover.
const idWait = time.Second

// idPause waits between two reads of an id file that has no id yet. Tests
// replace it to count the waits.
var idPause = time.Sleep

// errEmptyID is what readIDFile returns for a file with no id in it that
// no process is still writing.
var errEmptyID = errors.New("the id file is empty")

// readIDFile reads the id and the fingerprint in the file at path. A file
// that is there but empty is being written by a process that started at
// the same moment: it is read again for a while. Once it is older than
// idWait it is a leftover (a crash or a full disk between create and write)
// and the error is errEmptyID. A file of only white space counts as empty.
func readIDFile(path string) (id, fingerprint string, err error) {
	for wait := 0; ; wait++ {
		data, err := readIDBytes(path)
		if err != nil {
			return "", "", err
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 {
			// A modification time in the future cannot age, so the loop's
			// own count bounds the wait too.
			if wait < 20 && !olderThan(path, idWait) {
				idPause(50 * time.Millisecond)
				continue
			}
			return "", "", errEmptyID
		}
		if len(fields) > 2 || !validID(fields[0]) {
			return "", "", fmt.Errorf("%s does not hold an install id; remove it and nearhost makes a new one", path)
		}
		if len(fields) == 2 {
			fingerprint = fields[1]
		}
		return fields[0], fingerprint, nil
	}
}

// idReadEvery and idReadFor bound how long a read of the id file waits out
// another process replacing it.
const (
	idReadEvery = 20 * time.Millisecond
	idReadFor   = 2 * time.Second
)

// idReadFile reads the id file. Tests replace it.
var idReadFile = os.ReadFile

// readIDBytes reads the file at path. On Windows, opening a file while
// another process renames a new one over it fails with a sharing
// violation, which passes once the rename is done: that is read again,
// every idReadEvery for up to idReadFor.
func readIDBytes(path string) ([]byte, error) {
	data, err := idReadFile(path)
	for waited := time.Duration(0); err != nil && fsretry.Sharing(err) && waited < idReadFor; waited += idReadEvery {
		time.Sleep(idReadEvery)
		data, err = idReadFile(path)
	}
	return data, err
}

// olderThan reports whether the file at path was last written more than d
// ago. A file that cannot be examined counts as old.
func olderThan(path string, d time.Duration) bool {
	info, err := os.Stat(path)
	return err != nil || time.Since(info.ModTime()) >= d
}

// replaceEmptyID gives a leftover empty id file an id. Processes that find
// the same leftover take the id lock (the file id.lock beside the id, the
// one createID holds while it writes) and each looks again under it, so
// the first writes an id and the rest read it, and a creator that is slow
// is waited for. Nothing is removed: the file is replaced whole by a
// rename, so there is no moment with no file. On Windows a rename can fail
// while another program holds the file open, after the retries in
// WriteFileAtomic; the error then says so. Nothing reached while the lock
// is held may take it again, so the cases here write the file themselves.
func replaceEmptyID(path string, fingerprint func() string) (string, error) {
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return "", err
	}
	data, err := readIDBytes(path)
	if err == nil && len(strings.Fields(string(data))) > 0 {
		// Another process got here first.
		unlock()
		return installIDFor(path, fingerprint)
	}
	defer unlock()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	id := newID()
	return id, writeID(path, id, fingerprint())
}

// createID makes the id file. Two processes starting at once must end up
// with one id: the first to create the file wins, and the other reads what
// it wrote. The create and the write happen under the id lock, so a
// process that finds the file empty and old (see replaceEmptyID) waits for
// the lock and sees the id, however long this one stalled. The lock is
// released before the id is read back, since taking it again would wait on
// itself.
func createID(path string, fingerprint func() string) (string, error) {
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		unlock()
		return installIDFor(path, fingerprint)
	}
	if err != nil {
		unlock()
		return "", err
	}
	defer unlock()
	id := newID()
	_, err = f.WriteString(idLine(id, fingerprint()))
	return id, errors.Join(err, f.Close())
}

// writeID replaces the id file, whole: a reader never sees half of it.
func writeID(path, id, fingerprint string) error {
	return WriteFileAtomic(path, []byte(idLine(id, fingerprint)), 0o600)
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
