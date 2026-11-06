//go:build windows

package proxy

// withTrustedSudo runs fn. Windows has no sudo: the trust store library adds
// the CA to the current user's store with its own dialog.
func withTrustedSudo(fn func() error) error { return fn() }
