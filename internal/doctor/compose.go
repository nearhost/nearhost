package doctor

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/discovery"
)

// service is what the container checks need to know of one Compose service.
type service struct {
	Name        string
	Line        int               // line of the service's key
	Env         map[string]string // environment, with ${VAR} expanded
	EnvFiles    []string          // env_file paths
	ExtraHosts  []string          // names in extra_hosts, lowercased
	Sources     []string          // volume sources, expanded
	HostNetwork bool              // network_mode: host
}

// composeServices reads the services of a Compose file, sorted by name.
// Compose accepts environment, extra_hosts, env_file and volumes in several
// forms, so each is read from the yaml.Node rather than a fixed Go type;
// reading one form only would report a correct project as broken. Variables
// are expanded from env, and one that has no value stays as written.
func composeServices(data []byte, env map[string]string) ([]service, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, nil
	}
	services := mapGet(root.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}
	expand := func(s string) string {
		if v, _, ok := discovery.Expand(s, env); ok {
			return v
		}
		return s
	}

	var out []service
	for i := 0; i+1 < len(services.Content); i += 2 {
		key, node := services.Content[i], resolve(services.Content[i+1])
		s := service{Name: key.Value, Line: key.Line, Env: map[string]string{}}
		if node.Kind != yaml.MappingNode {
			out = append(out, s)
			continue
		}
		s.HostNetwork = scalar(mapGet(node, "network_mode")) == "host"

		for k, v := range keyValues(mapGet(node, "environment")) {
			switch {
			case v == nil:
				// A bare name takes its value from the environment Compose runs in.
				if e, ok := env[k]; ok {
					s.Env[k] = e
				}
			default:
				s.Env[k] = expand(*v)
			}
		}
		for k := range keyValues(mapGet(node, "extra_hosts")) {
			// The list form is "name:address" or "name=address"; the map form
			// is already split.
			name := k
			if i := strings.IndexAny(k, ":="); i >= 0 {
				name = k[:i]
			}
			s.ExtraHosts = append(s.ExtraHosts, strings.ToLower(strings.TrimSpace(name)))
		}
		sort.Strings(s.ExtraHosts)

		for _, n := range seq(mapGet(node, "env_file")) {
			if n.Kind == yaml.MappingNode {
				n = mapGet(n, "path")
			}
			if p := scalar(n); p != "" {
				s.EnvFiles = append(s.EnvFiles, p)
			}
		}
		for _, n := range seq(mapGet(node, "volumes")) {
			src := ""
			if n.Kind == yaml.MappingNode {
				src = scalar(mapGet(n, "source"))
			} else if before, _, found := strings.Cut(scalar(n), ":"); found {
				src = before
			}
			if src != "" {
				s.Sources = append(s.Sources, expand(src))
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// resolve follows an alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// mapGet returns the value of key in a mapping node, or nil. A key the
// mapping does not hold itself is looked up in its "<<" merge entries, which
// Compose files use to share settings through an anchor. The mapping's own
// keys win, and an earlier merge entry wins over a later one, as in YAML.
func mapGet(n *yaml.Node, key string) *yaml.Node {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key && n.Content[i].Tag != "!!merge" {
			return resolve(n.Content[i+1])
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Tag != "!!merge" {
			continue
		}
		v := resolve(n.Content[i+1])
		if v == nil {
			continue
		}
		sources := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			sources = v.Content
		}
		for _, m := range sources {
			if got := mapGet(m, key); got != nil {
				return got
			}
		}
	}
	return nil
}

// scalar returns the value of a scalar node, or "".
func scalar(n *yaml.Node) string {
	if n = resolve(n); n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// seq returns the items of a sequence node. A lone scalar counts as a list of
// one, as env_file allows.
func seq(n *yaml.Node) []*yaml.Node {
	n = resolve(n)
	switch {
	case n == nil:
		return nil
	case n.Kind == yaml.SequenceNode:
		return slices.Collect(func(yield func(*yaml.Node) bool) {
			for _, c := range n.Content {
				if !yield(resolve(c)) {
					return
				}
			}
		})
	case n.Kind == yaml.ScalarNode:
		return []*yaml.Node{n}
	}
	return nil
}

// keyValues reads a map or a list of "K=V" strings into key to value. A list
// entry with no "=" and a map entry with a null value give a nil value.
func keyValues(n *yaml.Node) map[string]*string {
	n = resolve(n)
	out := map[string]*string{}
	if n == nil {
		return out
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			v := resolve(n.Content[i+1])
			if v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
				out[n.Content[i].Value] = nil
				continue
			}
			s := v.Value
			out[n.Content[i].Value] = &s
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			k, v, found := strings.Cut(scalar(c), "=")
			if k == "" {
				continue
			}
			if !found {
				out[k] = nil
				continue
			}
			out[k] = &v
		}
	}
	return out
}
