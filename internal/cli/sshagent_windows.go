//go:build windows

package cli

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// sshAgentService is the production query for doctor.
var sshAgentService = querySSHAgentService

// querySSHAgentService reads the state of the ssh-agent service. It asks the
// service manager only for SC_MANAGER_CONNECT and the service only for the
// rights to read it, so it works without elevation.
func querySSHAgentService() (string, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	m := &mgr.Mgr{Handle: h}
	defer m.Disconnect()
	// mgr.Mgr.OpenService asks for full access, which needs elevation, so
	// open the service with the two query rights and wrap the handle.
	name, err := windows.UTF16PtrFromString("ssh-agent")
	if err != nil {
		return "", err
	}
	sh, err := windows.OpenService(h, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "missing", nil
		}
		return "", err
	}
	s := &mgr.Service{Name: "ssh-agent", Handle: sh}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return "", err
	}
	if cfg.StartType == mgr.StartDisabled {
		return "disabled", nil
	}
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	if st.State == windows.SERVICE_RUNNING {
		return "running", nil
	}
	return "stopped", nil
}
