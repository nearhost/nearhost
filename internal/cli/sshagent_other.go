//go:build !windows

package cli

// sshAgentService is nil off Windows: only Windows runs the ssh-agent
// service that doctor checks.
var sshAgentService func() (state string, err error)
