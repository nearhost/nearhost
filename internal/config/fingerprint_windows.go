package config

import "golang.org/x/sys/windows/registry"

// machineID is the MachineGuid Windows sets at install, or "" when it cannot
// be read. The 64-bit view is asked for so a 32-bit build reads the same key.
func machineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return guid
}
