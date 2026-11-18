package discovery

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
)

// portVarLabel reports whether name is a port variable: split on "_" and
// "-", one of its parts is PORT or PORTS in any case. The label is the
// other parts in lower case, joined by "-", or "port" when none are left.
func portVarLabel(name string) (string, bool) {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	var rest []string
	found := false
	for _, part := range parts {
		if strings.EqualFold(part, "PORT") || strings.EqualFold(part, "PORTS") {
			found = true
			continue
		}
		rest = append(rest, strings.ToLower(part))
	}
	if !found {
		return "", false
	}
	if len(rest) == 0 {
		return "port", true
	}
	return strings.Join(rest, "-"), true
}

// envPortValue parses a port variable's value: a whole decimal number from
// 1 to 65535, with surrounding spaces allowed.
func envPortValue(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, false
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// envPorts proposes one candidate per port that a port variable in env
// holds, sorted by port. When several variables hold the same port, the
// name that sorts first gives the label and the others join it in brackets.
func envPorts(env map[string]string) []Candidate {
	byPort := map[int][]string{}
	for name, value := range env {
		if _, ok := portVarLabel(name); !ok {
			continue
		}
		if port, ok := envPortValue(value); ok {
			byPort[port] = append(byPort[port], name)
		}
	}
	out := make([]Candidate, 0, len(byPort))
	for _, port := range slices.Sorted(maps.Keys(byPort)) {
		names := byPort[port]
		slices.Sort(names)
		label, _ := portVarLabel(names[0])
		out = append(out, Candidate{
			Port:   port,
			Role:   config.RoleIgnore,
			Label:  fmt.Sprintf("%s (%s)", label, strings.Join(names, ", ")),
			Source: "env:" + names[0],
			Var:    names[0],
		})
	}
	return out
}
