// Package dockernet asks the local Docker daemon which networks it has, so
// a project can be served to the containers on them and to nothing else.
package dockernet

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/oserr"
)

// timeout bounds asking Docker, over every endpoint tried. A daemon that is
// slower than this is treated as not answering; a start waits on it.
const timeout = 2 * time.Second

// maxBody bounds the network list read from the daemon.
const maxBody = 16 << 20

// Ranges returns the subnets of the local Docker daemon's networks, IPv4
// and IPv6, deduplicated and sorted with IPv4 first. It tries each place a
// daemon listens, in order (see endpoints), and uses the first that answers.
// It returns an error when none answers with a network list within 2s. An
// empty list with no error means the daemon has no subnets.
//
// Only local bridge networks count: a macvlan or ipvlan network's subnet is
// the real LAN, and an overlay network spans other hosts, so letting their
// addresses in would let those machines reach a project's sites.
func Ranges(ctx context.Context) ([]string, error) {
	return rangesFrom(ctx, endpoints())
}

// Source is Ranges that remembers which networks it leaves out. LeftOut
// lists them, and Logf, when set, announces each one when it first appears
// among them. A Source is safe for concurrent use; the zero value is ready.
type Source struct {
	// Logf announces a network newly left out, once while it stays left
	// out. It must not call back into the Source.
	Logf func(format string, args ...any)

	mu   sync.Mutex
	left map[string]string // networks left out by the last answer: key to sentence
}

// LeftOut returns a sentence for each network the last answer left out,
// sorted. Names and drivers come from the daemon, so they are quoted. It is
// nil before an answer and for a nil Source.
func (s *Source) LeftOut() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.left))
	for _, sentence := range s.left {
		out = append(out, sentence)
	}
	slices.Sort(out)
	return out
}

// Ranges is the package's Ranges, logging each network left out once.
func (s *Source) Ranges(ctx context.Context) ([]string, error) {
	return s.rangesFrom(ctx, endpoints())
}

// contextHost returns the host of the current docker context's endpoint, as
// written (unix://…, npipe://…, tcp://…), or "" when there is none.
// DOCKER_CONTEXT overrides the config file's currentContext, and
// DOCKER_CONFIG replaces ~/.docker. It is best effort: a missing or
// unparsable file and the "default" context give "".
func contextHost(home string) string {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, ".docker")
	}
	name := os.Getenv("DOCKER_CONTEXT")
	if name == "" {
		var cfg struct {
			CurrentContext string `json:"currentContext"`
		}
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil || json.Unmarshal(data, &cfg) != nil {
			return ""
		}
		name = cfg.CurrentContext
	}
	if name == "" || name == "default" {
		return ""
	}
	sum := sha256.Sum256([]byte(name))
	data, err := os.ReadFile(filepath.Join(dir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		Endpoints struct {
			Docker struct{ Host string }
		}
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.Endpoints.Docker.Host
}

// pipePath turns a docker host naming a named pipe on this machine, such as
// npipe:////./pipe/docker_engine, into the pipe's Windows path,
// \\.\pipe\docker_engine. Docker writes the pipe with either two or one
// leading slashes after the scheme, so both are taken. It reports false for
// any other host, including a pipe on another machine: that daemon's
// networks are not on this one.
func pipePath(host string) (string, bool) {
	rest, ok := strings.CutPrefix(host, "npipe://")
	if !ok {
		return "", false
	}
	parts := strings.SplitN(strings.TrimLeft(rest, "/"), "/", 3)
	if len(parts) != 3 || parts[0] != "." || !strings.EqualFold(parts[1], "pipe") || parts[2] == "" {
		return "", false
	}
	return `\\.\pipe\` + strings.ReplaceAll(parts[2], "/", `\`), true
}

// unique returns list without repeats, keeping the first of each.
func unique(list []string) []string {
	var out []string
	for _, s := range list {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// rangesFrom is Ranges over the given endpoints, tried in order.
func rangesFrom(ctx context.Context, endpoints []string) ([]string, error) {
	return (*Source)(nil).rangesFrom(ctx, endpoints)
}

// RangesFrom is Ranges over the daemon at each of the given endpoints, tried
// in order, with this Source's memory of what it leaves out. Tests use it to
// point a Source at a fake daemon.
func (s *Source) RangesFrom(ctx context.Context, endpoints []string) ([]string, error) {
	return s.rangesFrom(ctx, endpoints)
}

// rangesFrom is Source.Ranges over the given endpoints. A nil Source logs
// nothing.
func (s *Source) rangesFrom(ctx context.Context, endpoints []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var failed error // the first endpoint that was there but did not answer well
	for _, path := range endpoints {
		nets, err := ask(ctx, path)
		if err == nil {
			return s.ranges(nets), nil
		}
		if failed == nil && !oserr.NothingListening(err) {
			failed = fmt.Errorf("%s: %w", path, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if failed != nil {
		return nil, failed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no Docker daemon at %s", strings.Join(endpoints, ", "))
}

// network is the part of a network in the daemon's list that is read.
type network struct {
	Name   string
	Driver string
	Scope  string
	IPAM   struct {
		Config []struct {
			Subnet string
		}
	}
}

// ask fetches the network list from the daemon at endpoint (a unix socket's
// path, or a named pipe's on Windows) and returns its networks.
func ask(ctx context.Context, endpoint string) ([]network, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dial(ctx, endpoint)
			},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/networks", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker answered %s", resp.Status)
	}
	var nets []network
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&nets); err != nil {
		return nil, fmt.Errorf("read Docker's networks: %w", err)
	}
	return nets, nil
}

// isClientNetwork reports whether n's clients are let in: a bridge network
// whose scope is this host. Docker Desktop and Podman's Docker-compatible
// API report their bridge networks the same way.
func (n network) isClientNetwork() bool {
	return n.Driver == "bridge" && n.Scope == "local"
}

// ranges returns the subnets of the networks whose clients are let in, as
// Ranges does: masked, deduplicated and sorted, IPv4 first. Empty and
// unparsable subnets are skipped. A network with a subnet that is left out
// is remembered for LeftOut and announced through s.Logf when it is new to
// the list; one with none, such as Docker's host and none networks, is
// ignored.
func (s *Source) ranges(nets []network) []string {
	var prefixes []netip.Prefix
	left := map[string]string{}
	for _, n := range nets {
		var own []netip.Prefix
		for _, c := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(c.Subnet); err == nil {
				own = append(own, p.Masked())
			}
		}
		if len(own) == 0 {
			continue
		}
		if n.isClientNetwork() {
			prefixes = append(prefixes, own...)
			continue
		}
		left[n.Name+"\x00"+n.Driver] = fmt.Sprintf("docker network %q (%q) is not a local bridge; its clients are not let in", n.Name, n.Driver)
	}
	s.setLeftOut(left)
	// Addr.Compare puts IPv4 before IPv6, then orders by address.
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
	prefixes = slices.Compact(prefixes)
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = p.String()
	}
	return out
}

// setLeftOut records left as the networks now left out and announces those
// that were not before. A network that leaves the list is forgotten, which
// bounds the set, and is announced again if it returns.
func (s *Source) setLeftOut(left map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	var added []string
	for k, sentence := range left {
		if _, was := s.left[k]; !was {
			added = append(added, sentence)
		}
	}
	s.left = left
	logf := s.Logf
	s.mu.Unlock()
	if logf == nil {
		return
	}
	slices.Sort(added)
	for _, sentence := range added {
		logf("%s", sentence)
	}
}
