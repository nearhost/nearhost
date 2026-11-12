//go:build unix

package hosts

// flushDNS does nothing on Unix: the resolver reads the hosts file afresh.
// Tests replace it to count flushes.
var flushDNS = func() error { return nil }
