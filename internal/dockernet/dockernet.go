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
func Ranges(ctx context.Context) ([]string, error) {
	return rangesFrom(ctx, endpoints())
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var failed error // the first endpoint that was there but did not answer well
	for _, path := range endpoints {
		ranges, err := ask(ctx, path)
		if err == nil {
			return ranges, nil
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
	IPAM struct {
		Config []struct {
			Subnet string
		}
	}
}

// ask fetches the network list from the daemon at endpoint (a unix socket's
// path, or a named pipe's on Windows) and returns its subnets. Empty and
// unparsable subnets are skipped.
func ask(ctx context.Context, endpoint string) ([]string, error) {
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
	var prefixes []netip.Prefix
	for _, n := range nets {
		for _, c := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(c.Subnet); err == nil {
				prefixes = append(prefixes, p.Masked())
			}
		}
	}
	// Addr.Compare puts IPv4 before IPv6, then orders by address.
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
	prefixes = slices.Compact(prefixes)
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = p.String()
	}
	return out, nil
}
