package cli

import (
	"bufio"
	"bytes"
	"strings"
)

// sshHosts lists the host aliases an OpenSSH client config names in its
// Host lines, in order and without repeats. Patterns (with *, ? or a leading
// !) are left out: they name no one host. Include and Match are not
// followed.
func sshHosts(data []byte) []string {
	var hosts []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "Host a b", "Host=a b" and "host a" are all Host lines.
		key, rest, ok := strings.Cut(strings.Replace(line, "=", " ", 1), " ")
		if !ok || !strings.EqualFold(key, "host") {
			continue
		}
		for _, h := range strings.Fields(rest) {
			h = strings.Trim(h, `"`)
			if h == "" || strings.ContainsAny(h, "*?") || strings.HasPrefix(h, "!") || seen[h] {
				continue
			}
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}
