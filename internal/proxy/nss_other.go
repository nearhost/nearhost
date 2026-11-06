//go:build !windows

package proxy

// nssNeeded is whether a browser here keeps its own NSS store: Firefox
// does, and on Linux Chromium-family browsers share one.
const nssNeeded = true
