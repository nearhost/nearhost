//go:build windows

package proxy

// withTrustedSudo runs fn. Windows needs no elevation for trust, so there is
// no UAC prompt: the trust store library adds the CA to the current user's
// root store, and Windows shows its own security warning to confirm it.
func withTrustedSudo(fn func() error) error { return fn() }
