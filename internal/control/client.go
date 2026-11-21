package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// ErrNotRunning is returned when nothing answers on the control socket.
var ErrNotRunning = errors.New("Nearhost is not running")

// callTimeout bounds every call, unless the context's deadline is sooner.
const callTimeout = 5 * time.Second

// Client calls the control API of a running core. Its methods are safe for
// concurrent use.
type Client struct {
	http *http.Client
}

// newClient returns a client for the socket at path. The URLs it requests have
// the host nearhost, which the transport ignores: it dials the socket.
func newClient(path string) *Client {
	var d net.Dialer
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.DialContext(ctx, "unix", path)
			},
			DisableKeepAlives: true,
		},
	}}
}

// Dial returns a client for the core with the run directory runDir. It makes
// one request to prove that the socket answers, and returns ErrNotRunning
// when nothing does, including when the socket is left over from a core that
// has gone.
func Dial(runDir string) (*Client, error) {
	c := newClient(SocketPath(runDir))
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if _, err := c.Info(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// raw makes one request and returns the status code and body of the answer.
func (c *Client) raw(ctx context.Context, method, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://nearhost"+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if nobodyListens(err) {
			return 0, nil, ErrNotRunning
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// call makes a request that must answer want, and decodes a JSON answer into
// out unless out is nil.
func (c *Client) call(ctx context.Context, method, path string, want int, out any) error {
	code, body, err := c.raw(ctx, method, path)
	if err != nil {
		return err
	}
	if code != want {
		var e errorBody
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("the core answered %d: %s", code, e.Error)
		}
		return fmt.Errorf("the core answered %d", code)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("the core's answer is not understood: %w", err)
	}
	return nil
}

// Info returns what the core says it is.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var info Info
	err := c.call(ctx, http.MethodGet, "/v1", http.StatusOK, &info)
	return info, err
}

// Status returns the core's servers and projects.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.call(ctx, http.MethodGet, "/v1/status", http.StatusOK, &st)
	return st, err
}

// Stop asks the core to shut down. It returns once the core has begun, not
// when it has finished.
func (c *Client) Stop(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/stop", http.StatusAccepted, nil)
}

// HostsChanged tells the core that the hosts file has changed, so that it
// retries the projects that failed on it.
func (c *Client) HostsChanged(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/hosts/changed", http.StatusNoContent, nil)
}
