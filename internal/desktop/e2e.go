//go:build e2e

package desktop

import (
	"errors"
	"os"
)

// This file is in builds made with the e2e tag alone, which the journeys in
// internal/e2e make and no release does. In such a build no browser opens:
// each URL opened is written, a line each, to the file $NEARHOST_E2E_OPENED
// names, and opening fails when it names none.
func init() {
	standIn = func(url string) error {
		path := os.Getenv("NEARHOST_E2E_OPENED")
		if path == "" {
			return errors.New("the e2e build opens no browser: NEARHOST_E2E_OPENED is not set")
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(url + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
}
