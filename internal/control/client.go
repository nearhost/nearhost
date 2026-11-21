package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// ErrNotRunning is returned when nothing answers on the control socket.
var ErrNotRunning = errors.New("Nearhost is not running")

// callTimeout bounds every call, unless the context's deadline is sooner.
// For the event stream it bounds only the wait for the answer to begin.
var callTimeout = 5 * time.Second

// maxEnvelope is the longest line of the event stream the client reads. An
// event can carry a whole exchange, so it is generous.
const maxEnvelope = 1 << 20

// errStreamEnded is why a stream the core closed ended.
var errStreamEnded = errors.New("the core ended the event stream")

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
	return c.send(ctx, method, path, nil, true)
}

// send makes one request with data as its JSON body unless that is nil, and
// returns the status code and body of the answer. The call is bounded by
// callTimeout when capped is set, and by ctx alone when not.
func (c *Client) send(ctx context.Context, method, path string, data []byte, capped bool) (int, []byte, error) {
	if capped {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	var rd io.Reader
	if data != nil {
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://nearhost"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
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
		return answerError(code, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("the core's answer is not understood: %w", err)
	}
	return nil
}

// answerError describes an answer with the status code code and the body
// body that the caller did not want.
func answerError(code int, body []byte) error {
	var e errorBody
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("the core answered %d: %s", code, e.Error)
	}
	return fmt.Errorf("the core answered %d", code)
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

// State returns everything a client mirrors, as of the envelope numbered
// State.Seq.
func (c *Client) State(ctx context.Context) (State, error) {
	var st State
	err := c.call(ctx, http.MethodGet, "/v1/state", http.StatusOK, &st)
	return st, err
}

// Events streams the envelopes numbered after after, until ctx ends or the
// stream breaks. The channel closes then, and err, which waits for that,
// says why: nil when ctx ended. A reset on the channel means envelopes were
// missed: the caller fetches State again and goes on with the envelopes
// numbered after the reset's. Events returns ErrNotRunning when nothing
// answers on the socket.
func (c *Client) Events(ctx context.Context, after uint64) (ch <-chan Envelope, err func() error, e error) {
	ctx, cancel := context.WithCancel(ctx)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, "http://nearhost/v1/events?after="+strconv.FormatUint(after, 10), nil)
	if rerr != nil {
		cancel()
		return nil, nil, rerr
	}
	// The stream has no end in time, but its answer begins at once.
	timer := time.AfterFunc(callTimeout, cancel)
	resp, rerr := c.http.Do(req)
	if !timer.Stop() {
		if rerr == nil {
			resp.Body.Close()
		}
		cancel()
		return nil, nil, fmt.Errorf("the core did not answer within %v", callTimeout)
	}
	if rerr != nil {
		cancel()
		if nobodyListens(rerr) {
			return nil, nil, ErrNotRunning
		}
		return nil, nil, rerr
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		return nil, nil, answerError(resp.StatusCode, body)
	}
	out := make(chan Envelope, 64)
	done := make(chan struct{})
	var why error
	go func() {
		defer close(done)
		defer close(out)
		defer cancel()
		defer resp.Body.Close()
		why = readStream(ctx, resp.Body, out)
		if ctx.Err() != nil {
			why = nil
		}
	}()
	return out, func() error { <-done; return why }, nil
}

// readStream sends the envelopes of the server-sent events in r to out until
// r ends or ctx does, and returns why it stopped.
func readStream(ctx context.Context, r io.Reader, out chan<- Envelope) error {
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64<<10), maxEnvelope)
	var data []byte
	for lines.Scan() {
		line := lines.Bytes()
		switch {
		case len(line) == 0:
			// A blank line ends an event; one that had no data is nothing.
			if data == nil {
				continue
			}
			var e Envelope
			if err := json.Unmarshal(data, &e); err != nil {
				return fmt.Errorf("the core sent an envelope that is not understood: %w", err)
			}
			data = nil
			select {
			case out <- e:
			case <-ctx.Done():
				return ctx.Err()
			}
		case line[0] == ':':
			// A comment, such as a keepalive.
		case bytes.HasPrefix(line, []byte("data:")):
			v := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
			// Not nil once a data line is read, even an empty one.
			if data == nil {
				data = []byte{}
			} else {
				data = append(data, '\n')
			}
			data = append(data, v...)
		}
	}
	if err := lines.Err(); err != nil {
		return fmt.Errorf("the event stream broke: %w", err)
	}
	return errStreamEnded
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
