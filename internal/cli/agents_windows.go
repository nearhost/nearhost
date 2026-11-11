//go:build windows

package cli

// serverAgents is whether coding agents on servers can reach Nearhost. Not
// yet on Windows: ssh cannot forward a socket whose local path holds a
// drive's colon, so sessions forward none.
const serverAgents = false
