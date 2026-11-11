//go:build unix

package cli

// serverAgents is whether coding agents on servers can reach Nearhost: each
// session forwards them a socket.
const serverAgents = true
