//go:build !windows

package proxy

// sharedSockets reports whether every configuration Caddy loads shares one
// socket per address. On Unix each binds its own, with SO_REUSEPORT.
const sharedSockets = false
