package doctor

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/discovery"
)

// service is what the container checks need to know of one Compose service.
type service struct {
	Name        string
	File        string            // the file a fix for the service goes in
	Line        int               // line of the service's key in File
	Env         map[string]string // environment, with ${VAR} expanded
	EnvFiles    []string          // env_file paths
	ExtraHosts  []string          // names in extra_hosts, lowercased
	Sources     []string          // volume sources, expanded from the shell and .env only
	HostNetwork bool              // network_mode: host
	NetworkSet  bool              // network_mode is set, to host or not
}

// composeServices reads the services of a Compose file, sorted by name.
// Compose accepts environment, extra_hosts, env_file and volumes in several
// forms, so each is read from the yaml.Node rather than a fixed Go type;
// reading one form only would report a correct project as broken. Variables
// are expanded from env, and volume sources from dotEnv, the variables of
// the shell and .env alone: Compose fills in the file from those, never
// .env.local, so a mount that needs .env.local is not one. A value with a
// variable that has no value stays as written. A file whose "<<" merges nest
// deeper than maxMergeDepth, or repeat past maxMergeLookups, gives
// errComposeTooDeep.
func composeServices(data []byte, env, dotEnv map[string]string) ([]service, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, nil
	}
	w := &walker{}
	services := w.mapGet(root.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}
	expand := func(s string) string { return expandCompose(s, env) }

	var out []service
	for _, kv := range w.mapPairs(services) {
		key, node := kv[0], resolve(kv[1])
		s := service{Name: key.Value, Line: key.Line, Env: map[string]string{}}
		if node.Kind != yaml.MappingNode {
			out = append(out, s)
			continue
		}
		if mode := w.mapGet(node, "network_mode"); mode != nil {
			s.NetworkSet = true
			s.HostNetwork = scalar(mode) == "host"
		}

		for k, v := range w.keyValues(w.mapGet(node, "environment")) {
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
		for k := range w.keyValues(w.mapGet(node, "extra_hosts")) {
			// The list form is "name:address" or "name=address"; the map form
			// is already split.
			name := k
			if i := strings.IndexAny(k, ":="); i >= 0 {
				name = k[:i]
			}
			s.ExtraHosts = append(s.ExtraHosts, strings.ToLower(strings.TrimSpace(name)))
		}
		sort.Strings(s.ExtraHosts)

		for _, n := range seq(w.mapGet(node, "env_file")) {
			if n.Kind == yaml.MappingNode {
				n = w.mapGet(n, "path")
			}
			if p := scalar(n); p != "" {
				s.EnvFiles = append(s.EnvFiles, p)
			}
		}
		for _, n := range seq(w.mapGet(node, "volumes")) {
			src := ""
			if n.Kind == yaml.MappingNode {
				src = scalar(w.mapGet(n, "source"))
			} else {
				src = volumeSource(scalar(n))
			}
			if src != "" {
				s.Sources = append(s.Sources, expandCompose(src, dotEnv))
			}
		}
		out = append(out, s)
	}
	if w.tooDeep {
		return nil, errComposeTooDeep
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// mergeServices merges the services of an override file over those of the
// Compose file, as Compose does: extra_hosts, env_file and volumes add to
// the service's, environment replaces by key, and network_mode replaces.
// A service the override names takes the override's file and line, as an
// addition made there joins the rest; one it does not name keeps its own.
// The result is sorted by name.
func mergeServices(base, override []service) []service {
	out := slices.Clone(base)
	at := map[string]int{}
	for i, s := range out {
		at[s.Name] = i
	}
	for _, o := range override {
		i, ok := at[o.Name]
		if !ok {
			out = append(out, o)
			continue
		}
		s := &out[i]
		env := maps.Clone(s.Env)
		maps.Copy(env, o.Env)
		s.Env = env
		s.EnvFiles = slices.Concat(s.EnvFiles, o.EnvFiles)
		s.ExtraHosts = slices.Compact(slices.Sorted(slices.Values(slices.Concat(s.ExtraHosts, o.ExtraHosts))))
		s.Sources = slices.Concat(s.Sources, o.Sources)
		if o.NetworkSet {
			s.HostNetwork, s.NetworkSet = o.HostNetwork, true
		}
		s.File, s.Line = o.File, o.Line
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// requiredVar matches ${VAR:?message} and ${VAR?message}, which Compose
// fills in like ${VAR} and refuses to run without.
var requiredVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?)\?[^}]*\}`)

// expandCompose substitutes the variables in s from env as Compose would,
// or returns s as written when one has no value. discovery.Expand reads
// ${VAR}, ${VAR:-default}, ${VAR-default} and $VAR; a required variable is
// filled in here first, ":?" only when it is not empty.
func expandCompose(s string, env map[string]string) string {
	ok := true
	filled := requiredVar.ReplaceAllStringFunc(s, func(m string) string {
		sub := requiredVar.FindStringSubmatch(m)
		v, set := env[sub[1]]
		if !set || (sub[2] == ":" && v == "") {
			ok = false
			return m
		}
		return v
	})
	if !ok {
		return s
	}
	if v, _, ok := discovery.Expand(filled, env); ok {
		return v
	}
	return s
}

// volumeSource is the source of a short volume, "source:target[:mode]".
// A colon inside ${...}, as in ${VAR:-default}, is part of the source.
func volumeSource(spec string) string {
	depth := 0
	for i, c := range spec {
		switch {
		case c == '{' && i > 0 && spec[i-1] == '$':
			depth++
		case c == '}' && depth > 0:
			depth--
		case c == ':' && depth == 0:
			return spec[:i]
		}
	}
	return ""
}

// resolve follows an alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// maxMergeDepth is how many "<<" merges deep a walk of a Compose file goes.
// The file comes with the repository, and a long chain of anchors that
// merge one another must not run doctor's walk on and on.
const maxMergeDepth = 64

// maxMergeLookups is how many lookups a walk of one Compose file makes,
// each mapGet or mapPairs on a mapping counting one, found or not. Merges
// that repeat an anchor, each level merging the one before twice, double
// the walk at every level, so depth alone does not bound it.
const maxMergeLookups = 100000

// errComposeTooDeep is composeServices' error for a Compose file whose
// merges nest deeper than maxMergeDepth or repeat past maxMergeLookups.
var errComposeTooDeep = errors.New("the compose file nests or repeats << merges too much to check")

// walker walks the nodes of one Compose file. It follows "<<" merge entries
// at most maxMergeDepth deep and makes no more than maxMergeLookups
// lookups, and records that it stopped at either.
type walker struct {
	lookups int
	tooDeep bool
}

// enter counts a lookup in a mapping reached through depth merges, and
// reports whether the walk may go on.
func (w *walker) enter(depth int) bool {
	w.lookups++
	if w.tooDeep || depth > maxMergeDepth || w.lookups > maxMergeLookups {
		w.tooDeep = true
		return false
	}
	return true
}

// mapGet returns the value of key in a mapping node, or nil. A key the
// mapping does not hold itself is looked up in its "<<" merge entries, which
// Compose files use to share settings through an anchor. The mapping's own
// keys win, and an earlier merge entry wins over a later one, as in YAML.
func (w *walker) mapGet(n *yaml.Node, key string) *yaml.Node {
	return w.mapGetAt(n, key, 0)
}

// mapGetAt is mapGet for a mapping reached through depth merges.
func (w *walker) mapGetAt(n *yaml.Node, key string, depth int) *yaml.Node {
	if !w.enter(depth) {
		return nil
	}
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
			if got := w.mapGetAt(m, key, depth+1); got != nil {
				return got
			}
		}
	}
	return nil
}

// mapPairs lists the key and value nodes of a mapping, merge entries
// included, with the same precedence as mapGet: own keys first, then each
// "<<" entry in order, the first occurrence of a key winning.
func (w *walker) mapPairs(n *yaml.Node) [][2]*yaml.Node {
	return w.mapPairsAt(n, 0)
}

// mapPairsAt is mapPairs for a mapping reached through depth merges.
func (w *walker) mapPairsAt(n *yaml.Node, depth int) [][2]*yaml.Node {
	if !w.enter(depth) {
		return nil
	}
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out [][2]*yaml.Node
	seen := map[string]bool{}
	add := func(k, v *yaml.Node) {
		if !seen[k.Value] {
			seen[k.Value] = true
			out = append(out, [2]*yaml.Node{k, v})
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Tag != "!!merge" {
			add(n.Content[i], n.Content[i+1])
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
			for _, kv := range w.mapPairsAt(m, depth+1) {
				add(kv[0], kv[1])
			}
		}
	}
	return out
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
func (w *walker) keyValues(n *yaml.Node) map[string]*string {
	n = resolve(n)
	out := map[string]*string{}
	if n == nil {
		return out
	}
	switch n.Kind {
	case yaml.MappingNode:
		for _, kv := range w.mapPairs(n) {
			v := resolve(kv[1])
			if v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
				out[kv[0].Value] = nil
				continue
			}
			s := v.Value
			out[kv[0].Value] = &s
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
