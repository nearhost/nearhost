//go:build !windows

package update

// StepAside does nothing off Windows, where an installer can replace a
// running binary.
func StepAside(exe string) (restore func() error, err error) {
	return func() error { return nil }, nil
}

// CleanOld does nothing off Windows, where nothing is moved aside.
func CleanOld(exe string) {}
