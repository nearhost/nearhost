package config

import "os/exec"

// machineID is this Mac's platform UUID, or "" when it cannot be read.
func machineID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	return platformUUID(string(out))
}
