package discovery

import (
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strconv"
)

// ViteInfo is what a light scan of vite.config.* reveals.
type ViteInfo struct {
	Found         bool
	Port          int
	AllowedHosts  []string
	AllowAllHosts bool
	HTTPS         bool
}

var viteFiles = []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts", "vite.config.cjs"}

var (
	vitePortRe         = regexp.MustCompile(`\bport\s*:\s*(\d+)`)
	viteAllowedHostsRe = regexp.MustCompile(`allowedHosts\s*:\s*(true|\[([^\]]*)\])`)
	viteQuotedRe       = regexp.MustCompile(`['"]([^'"]+)['"]`)
	viteHTTPSRe        = regexp.MustCompile(`\bhttps\s*:\s*(true|\{)`)
)

// readVite returns the first vite config file found.
func readVite(r FileReader) ([]byte, bool, error) {
	for _, name := range viteFiles {
		data, err := r.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return data, true, nil
	}
	return nil, false, nil
}

// ScanVite extracts server settings with regular expressions. It never
// executes or fully parses the config.
func ScanVite(data []byte) ViteInfo {
	info := ViteInfo{Found: true}
	if m := vitePortRe.FindSubmatch(data); m != nil {
		info.Port, _ = strconv.Atoi(string(m[1]))
	}
	if m := viteAllowedHostsRe.FindSubmatch(data); m != nil {
		if string(m[1]) == "true" {
			info.AllowAllHosts = true
		} else {
			for _, q := range viteQuotedRe.FindAllSubmatch(m[2], -1) {
				info.AllowedHosts = append(info.AllowedHosts, string(q[1]))
			}
		}
	}
	info.HTTPS = viteHTTPSRe.Match(data)
	return info
}

// hasVitePackage reports whether package.json depends on vite.
func hasVitePackage(r FileReader) bool {
	data, err := r.ReadFile("package.json")
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, a := pkg.Dependencies["vite"]
	_, b := pkg.DevDependencies["vite"]
	return a || b
}
