package transport

import (
	"os"
	"runtime"
)

// New returns an idle Transport for c. Call Start to connect.
//
// Windows' ssh has no ControlMaster, so it gets the per-process transport;
// elsewhere NEARHOST_TRANSPORT=per-process asks for it too. Any other value of
// the variable is ignored, and the ControlMaster Session is used.
func New(c Config) Transport {
	if runtime.GOOS == "windows" || os.Getenv("NEARHOST_TRANSPORT") == "per-process" {
		return newPerProcess(c)
	}
	s := NewSession(c.Name, c.Host, c.RunDir)
	if c.SSHPath != "" {
		s.SSHPath = c.SSHPath
	}
	s.Env = c.Env
	if c.Logf != nil {
		s.Logf = c.Logf
	}
	s.Foreground = c.Foreground
	return s
}
