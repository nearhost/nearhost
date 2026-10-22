// Package dockernet asks the local Docker daemon which networks it has, so
// a project can be served to the containers on them and to nothing else.
package dockernet

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Fallback is the range served while Docker has never answered: the private
// block Docker's default address pools come from. It also holds LAN
// addresses, so it is only a stopgap until Docker says which networks exist.
const Fallback = "172.16.0.0/12"

// timeout bounds asking Docker, over every socket tried. A daemon that is
// slower than this is treated as not answering; a start waits on it.
const timeout = 2 * time.Second

// maxBody bounds the network list read from the daemon.
const maxBody = 16 << 20

// Ranges returns the subnets of the local Docker daemon's networks, IPv4
// and IPv6, deduplicated and sorted with IPv4 first. It tries each place a
// daemon's socket lives, in order (see sockets), and uses the first that
// answers. It returns an error when none answers with a network list within
// 2s. An empty list with no error means the daemon has no subnets.
func Ranges(ctx context.Context) ([]string, error) {
	return rangesFrom(ctx, sockets())
}

// sockets lists where a daemon's socket may be, most specific first:
// DOCKER_HOST when it names a unix socket, Docker Desktop's, Colima's, a
// rootless daemon's and the system one. A DOCKER_HOST that is not a unix
// socket (tcp, ssh) is skipped: that daemon's networks are not on this
// machine, so its containers do not come from them.
func sockets() []string {
	var out []string
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && host != "" {
		out = append(out, host)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"))
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		out = append(out, filepath.Join(dir, "docker.sock"))
	}
	return append(out, "/var/run/docker.sock")
}

// rangesFrom is Ranges over the given sockets, tried in order.
func rangesFrom(ctx context.Context, sockets []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var failed error // the first socket that was there but did not answer well
	for _, path := range sockets {
		ranges, err := ask(ctx, path)
		if err == nil {
			return ranges, nil
		}
		if failed == nil && !absent(err) {
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
	return nil, fmt.Errorf("no Docker daemon at %s", strings.Join(sockets, ", "))
}

// absent reports whether err says nothing listens at a socket: there is no
// file, or one left behind by a daemon that has gone.
func absent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// network is the part of a network in the daemon's list that is read.
type network struct {
	IPAM struct {
		Config []struct {
			Subnet string
		}
	}
}

// ask fetches the network list from the daemon at path and returns its
// subnets. Empty and unparsable subnets are skipped.
func ask(ctx context.Context, path string) ([]string, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
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
