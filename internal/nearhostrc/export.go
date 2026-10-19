package nearhostrc

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/config"
)

// exportHeader tells whoever opens the file where it came from and that it is
// theirs to edit.
const exportHeader = "# Written by nearhost export. Safe to edit and commit; see https://nearhost.dev/docs for the format.\n"

// aliasVar is the variable an exported alias refers to when the project's env
// sets it to that alias.
const aliasVar = "APP_HOST"

// PortVars lists, for each port, the variables discovery says set it, in
// discovery's rank order.
type PortVars map[int][]string

// Export renders p as a .nearhostrc and returns its bytes with the snapshot
// that parsing and merging them gives, to save as the approved one. dir is the
// project directory's base name; an alias equal to config.AliasFor(dir) is
// left out, since reading the file without one gives the same name.
//
// A port is written as ${VAR:-port} when a variable from vars holds that port
// in env, so the file follows the project's .env on another machine, and as a
// bare number otherwise. Export refuses to return a file that does not read
// back to p's routes, main name and docker_clients choice, so what it writes
// is always what the user approved.
func Export(p config.Project, dir string, vars PortVars, env map[string]string) ([]byte, *config.RcSnapshot, error) {
	var b strings.Builder
	b.WriteString(exportHeader)
	b.WriteString("version: 1\n")

	if p.Alias != "" && p.Alias != config.AliasFor(dir) {
		alias, err := scalar(p.Alias, "main name")
		if err != nil {
			return nil, nil, err
		}
		if env[aliasVar] == p.Alias {
			// Quoting "${APP_HOST:-x}" keeps YAML from reading the braces.
			alias = "${" + aliasVar + ":-" + p.Alias + "}"
			if q, err := scalar(alias, "main name"); err == nil {
				alias = q
			}
		}
		b.WriteString("alias: " + alias + "\n")
	}

	b.WriteString("routes:\n")
	for _, r := range p.Routes {
		port := strconv.Itoa(r.Port)
		for _, v := range vars[r.Port] {
			if got, set := env[v]; !set || got == port {
				port = "${" + v + ":-" + port + "}"
				break
			}
		}
		b.WriteString("  - port: " + port + "\n")
		if r.Label != "" {
			label, err := scalar(r.Label, fmt.Sprintf("the label of port %d", r.Port))
			if err != nil {
				return nil, nil, err
			}
			b.WriteString("    label: " + label + "\n")
		}
		if r.Role != config.RoleWeb {
			b.WriteString("    role: " + string(r.Role) + "\n")
		}
		if len(r.Hosts) > 0 {
			hosts := make([]string, len(r.Hosts))
			for i, h := range r.Hosts {
				q, err := scalar(h, fmt.Sprintf("a host of port %d", r.Port))
				if err != nil {
					return nil, nil, err
				}
				hosts[i] = q
			}
			b.WriteString("    hosts: [" + strings.Join(hosts, ", ") + "]\n")
		}
	}
	if p.DockerClients() {
		b.WriteString("docker_clients: true\n")
	}
	data := []byte(b.String())

	f, err := Parse(data, env)
	if err != nil {
		return nil, nil, fmt.Errorf("export: the file it wrote does not read back: %w", err)
	}
	probe := p
	probe.Nearhostrc = nil
	merged, err := Merge(probe, f, p.Routes)
	if err != nil {
		return nil, nil, fmt.Errorf("export: %w", err)
	}
	if err := sameProject(p, merged); err != nil {
		return nil, nil, fmt.Errorf("export: the file it wrote would not read back to this project: %w", err)
	}
	return data, merged.Nearhostrc, nil
}

// scalar renders s as one YAML scalar that reads back as s: plain when YAML
// allows it, double-quoted otherwise. Text with a line break is refused,
// because the file is one line per value; what names the value in the error.
func scalar(s, what string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("export: %s has a line break, which a .nearhostrc cannot hold: %q", what, s)
	}
	n := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	out, err := yaml.Marshal(&n)
	if err != nil {
		return "", fmt.Errorf("export: %s: %w", what, err)
	}
	text := strings.TrimSuffix(string(out), "\n")
	if strings.HasPrefix(text, "'") || strings.HasPrefix(text, `"`) {
		n.Style = yaml.DoubleQuotedStyle
		if out, err = yaml.Marshal(&n); err != nil {
			return "", fmt.Errorf("export: %s: %w", what, err)
		}
		text = strings.TrimSuffix(string(out), "\n")
	}
	if strings.Contains(text, "\n") {
		return "", fmt.Errorf("export: %s does not fit on one line: %q", what, s)
	}
	return text, nil
}

// sameProject says what differs between want and got in the parts a
// .nearhostrc carries: routes, main name and docker_clients. Merge sets the
// snapshot's docker_clients from the file, so it is compared after the merge.
func sameProject(want, got config.Project) error {
	if want.Alias != got.Alias {
		return fmt.Errorf("main name %q became %q", want.Alias, got.Alias)
	}
	if want.DockerClients() != got.DockerClients() {
		return fmt.Errorf("docker_clients %t became %t", want.DockerClients(), got.DockerClients())
	}
	if len(want.Routes) != len(got.Routes) {
		return fmt.Errorf("%d routes became %d", len(want.Routes), len(got.Routes))
	}
	for i, w := range want.Routes {
		g := got.Routes[i]
		if w.Port != g.Port || w.Role != g.Role || w.Label != g.Label || strings.Join(w.Hosts, "\n") != strings.Join(g.Hosts, "\n") || len(w.Hosts) != len(g.Hosts) {
			return fmt.Errorf("port %d: %+v became %+v", w.Port, w, g)
		}
	}
	return nil
}
