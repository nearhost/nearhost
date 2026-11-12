//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/sys/windows/registry"
)

// firefoxRootsOff is the production check for doctor.
var firefoxRootsOff = checkFirefoxRoots

const firefoxPolicyKey = `SOFTWARE\Policies\Mozilla\Firefox\Certificates`

// checkFirefoxRoots reports whether Firefox is set not to read Windows'
// trusted roots, by policy or in a profile. Firefox has read them by default
// since version 120, so a Firefox that is absent or unset reports nothing.
func checkFirefoxRoots() (string, bool) {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, firefoxPolicyKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetIntegerValue("ImportEnterpriseRoots")
		k.Close()
		if err == nil && v == 0 {
			return "policy", true
		}
	}
	return firefoxProfilesOff(os.Getenv("APPDATA"))
}

var rootsPrefOff = regexp.MustCompile(`user_pref\(\s*["']security\.enterprise_roots\.enabled["']\s*,\s*false\s*\)`)

// firefoxProfilesOff looks in every profile under appdata for the preference
// set to false in prefs.js or user.js, and names the first profile that has
// it.
func firefoxProfilesOff(appdata string) (string, bool) {
	if appdata == "" {
		return "", false
	}
	profiles, _ := filepath.Glob(filepath.Join(appdata, "Mozilla", "Firefox", "Profiles", "*"))
	for _, dir := range profiles {
		for _, name := range []string{"prefs.js", "user.js"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil && rootsPrefOff.Match(data) {
				return "profile " + filepath.Base(dir), true
			}
		}
	}
	return "", false
}
