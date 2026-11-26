package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

// ErrRequestEnded is what answering a request that has already ended gives:
// an agent gave up on it, it expired, or someone else answered first.
var ErrRequestEnded = errors.New("the request has ended")

// ErrAllowNotSaved is what an answer of always gives when the request was
// answered but the Always allow grant could not be saved. It may come with
// ErrRequestEnded.
var ErrAllowNotSaved = errors.New("always allow was not saved")

// ErrNotFound is what asking for something the core does not have gives.
var ErrNotFound = errors.New("not found")

// ErrUnimplemented is what an action that this Nearhost cannot do yet gives.
// The server answers it with 501.
var ErrUnimplemented = errors.New("this Nearhost cannot do that yet")

// ErrNetworkOff is what asking to pair gives on a core that is not serving to
// the network.
var ErrNetworkOff = errors.New("Networked mode is off, so no device can pair.")

// ErrDeviceUnknown is what revoking a device that is not paired gives.
var ErrDeviceUnknown = errors.New("No paired device has that name.")

// PairInfo is a pairing code the core gave, with the address a device opens
// to use it.
type PairInfo struct {
	Code    string    `json:"code"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

// PairState is what became of a pairing code.
type PairState struct {
	// Device is the name of the device that paired with it.
	Device  string `json:"device"`
	Paired  bool   `json:"paired"`
	Expired bool   `json:"expired"`
}

// DeviceInfo is one paired device.
type DeviceInfo struct {
	Name     string    `json:"name"`
	Paired   time.Time `json:"paired"`
	LastSeen time.Time `json:"last_seen"`
	LastAddr string    `json:"last_addr"`
}

// ErrNoRoute is what asking the core for something it has no route for gives:
// it is running from an older release.
var ErrNoRoute = errors.New("the running Nearhost does not answer this; it is an older release")

// environmentTimeout is how long the core takes at most to look at what it
// can reach.
const environmentTimeout = 10 * time.Second

// Actions are the things the terminal interface asks of the core, one route
// each. A method that returns an error says why it could not do the action;
// an error for which errors.Is(err, ErrRequestEnded) says the request it was
// to answer had ended.
type Actions interface {
	StartProject(ctx context.Context, name string) error
	StopProject(ctx context.Context, name string) error
	Recheck(ctx context.Context, name string) error
	AcceptChange(name, sha string) error
	DeclineChange(name, sha string)
	ConnectServer(ctx context.Context, name string) error
	DisconnectServer(ctx context.Context, name string) error
	ReconnectServer(ctx context.Context, name string) error
	// Reload loads the config file and applies it now.
	Reload(ctx context.Context) error
	AcceptProposal(id, name string)
	DeclineProposal(id, reason string)
	// AnswerPassthrough answers a passthrough request with one of yes, no,
	// always or close, and returns once the passthrough has opened.
	AnswerPassthrough(ctx context.Context, id, answer string) error
	ClosePassthrough(ctx context.Context, id string) error
	// AnswerDocument answers a document request as AnswerPassthrough does.
	AnswerDocument(ctx context.Context, id, answer string) error
	OpenDocument(id string) error
	RemoveDocuments(id string) error
	RevokeGrant(server, requester, kind string) error
	// Captured returns the captured exchange with the given id, if it is
	// still kept.
	Captured(id string) (proxy.Exchange, bool)
	// Replay sends a captured request again and returns the status it got.
	Replay(ctx context.Context, id string) (int, error)
	// PageKeyURL is the address of the home page with its key, the /?k= one.
	PageKeyURL() (string, error)
	// Environment looks, when asked, at what the core can reach: opening
	// links, the SSH agent, Docker and ssh.
	Environment(ctx context.Context) ([]EnvCheck, error)
	// Pair gives a new pairing code. It returns ErrNetworkOff when the core
	// does not serve to the network.
	Pair() (PairInfo, error)
	// PairStatus says what became of a pairing code.
	PairStatus(code string) (PairState, error)
	// Devices lists the paired devices.
	Devices() ([]DeviceInfo, error)
	// RevokeDevice unpairs a device, or returns ErrDeviceUnknown.
	RevokeDevice(name string) error
	// RecheckFirewall asks the core to look for the Windows firewall rule
	// now, rather than at its next look. It does nothing where no rule is
	// needed or none is waited for.
	RecheckFirewall() error
}

// UnimplementedActions is an Actions that can do none of them: each action
// that returns an error returns ErrUnimplemented. One that returns no error
// cannot say so, and does nothing: its route answers as for success. Embed it
// in a Backend that does not do them yet; tests do.
type UnimplementedActions struct{}

var _ Actions = UnimplementedActions{}

func (UnimplementedActions) StartProject(context.Context, string) error { return ErrUnimplemented }
func (UnimplementedActions) StopProject(context.Context, string) error  { return ErrUnimplemented }
func (UnimplementedActions) Recheck(context.Context, string) error      { return ErrUnimplemented }
func (UnimplementedActions) AcceptChange(string, string) error          { return ErrUnimplemented }
func (UnimplementedActions) DeclineChange(string, string)               {}
func (UnimplementedActions) ConnectServer(context.Context, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) DisconnectServer(context.Context, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) ReconnectServer(context.Context, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) Reload(context.Context) error   { return ErrUnimplemented }
func (UnimplementedActions) AcceptProposal(string, string)  {}
func (UnimplementedActions) DeclineProposal(string, string) {}
func (UnimplementedActions) AnswerPassthrough(context.Context, string, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) ClosePassthrough(context.Context, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) AnswerDocument(context.Context, string, string) error {
	return ErrUnimplemented
}
func (UnimplementedActions) OpenDocument(string) error                { return ErrUnimplemented }
func (UnimplementedActions) RemoveDocuments(string) error             { return ErrUnimplemented }
func (UnimplementedActions) RevokeGrant(string, string, string) error { return ErrUnimplemented }
func (UnimplementedActions) Captured(string) (proxy.Exchange, bool)   { return proxy.Exchange{}, false }
func (UnimplementedActions) Replay(context.Context, string) (int, error) {
	return 0, ErrUnimplemented
}
func (UnimplementedActions) PageKeyURL() (string, error) { return "", ErrUnimplemented }
func (UnimplementedActions) Environment(context.Context) ([]EnvCheck, error) {
	return nil, ErrUnimplemented
}

func (UnimplementedActions) Pair() (PairInfo, error) { return PairInfo{}, ErrUnimplemented }
func (UnimplementedActions) PairStatus(string) (PairState, error) {
	return PairState{}, ErrUnimplemented
}
func (UnimplementedActions) Devices() ([]DeviceInfo, error) { return nil, ErrUnimplemented }
func (UnimplementedActions) RevokeDevice(string) error      { return ErrUnimplemented }
func (UnimplementedActions) RecheckFirewall() error         { return ErrUnimplemented }

// maxActionBody is the longest request body an action route reads.
const maxActionBody = 64 << 10

// actionErrorBody is the part of a failed action's body that clients before
// problem details read. Ended is set when the request to answer had ended,
// and AllowNotSaved when the answer's Always allow could not be saved; both
// may be. Both stay in the problem body as extension members.
type actionErrorBody struct {
	Error         string `json:"error"`
	Ended         bool   `json:"ended,omitempty"`
	AllowNotSaved bool   `json:"allowNotSaved,omitempty"`
}

// validAnswers are the answers a passthrough or document request takes.
var validAnswers = map[string]bool{"yes": true, "no": true, "always": true, "close": true}

// registerActions adds the action routes to mux.
func (s *Server) registerActions(mux *http.ServeMux) {
	noContent := func(f func(r *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := f(r); err != nil {
				failAction(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	// body decodes the request's JSON body into v, and answers 400 and
	// reports false when it cannot.
	body := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxActionBody)).Decode(v); err != nil {
			fail(w, http.StatusBadRequest, report.Errorf(report.ControlBadRequest, "the body is not the JSON this route takes: %s", err).Wrap(err))
			return false
		}
		return true
	}
	post := func(pattern string, h http.HandlerFunc) { mux.HandleFunc("POST "+pattern, h) }
	b := s.b

	post("/v1/projects/{name}/start", noContent(func(r *http.Request) error {
		return b.StartProject(r.Context(), r.PathValue("name"))
	}))
	post("/v1/projects/{name}/stop", noContent(func(r *http.Request) error {
		return b.StopProject(r.Context(), r.PathValue("name"))
	}))
	post("/v1/projects/{name}/recheck", noContent(func(r *http.Request) error {
		return b.Recheck(r.Context(), r.PathValue("name"))
	}))
	post("/v1/projects/{name}/change/accept", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SHA string `json:"sha"`
		}
		if !body(w, r, &in) {
			return
		}
		if err := b.AcceptChange(r.PathValue("name"), in.SHA); err != nil {
			failAction(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	post("/v1/projects/{name}/change/decline", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SHA string `json:"sha"`
		}
		if !body(w, r, &in) {
			return
		}
		b.DeclineChange(r.PathValue("name"), in.SHA)
		w.WriteHeader(http.StatusNoContent)
	})
	post("/v1/servers/{name}/connect", noContent(func(r *http.Request) error {
		return b.ConnectServer(r.Context(), r.PathValue("name"))
	}))
	post("/v1/servers/{name}/disconnect", noContent(func(r *http.Request) error {
		return b.DisconnectServer(r.Context(), r.PathValue("name"))
	}))
	post("/v1/servers/{name}/reconnect", noContent(func(r *http.Request) error {
		return b.ReconnectServer(r.Context(), r.PathValue("name"))
	}))
	post("/v1/config/reload", noContent(func(r *http.Request) error {
		return b.Reload(r.Context())
	}))
	post("/v1/proposals/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if !body(w, r, &in) {
			return
		}
		b.AcceptProposal(r.PathValue("id"), in.Name)
		w.WriteHeader(http.StatusNoContent)
	})
	post("/v1/proposals/{id}/decline", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string `json:"reason"`
		}
		if !body(w, r, &in) {
			return
		}
		b.DeclineProposal(r.PathValue("id"), in.Reason)
		w.WriteHeader(http.StatusNoContent)
	})
	answer := func(f func(ctx context.Context, id, answer string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Answer string `json:"answer"`
			}
			if !body(w, r, &in) {
				return
			}
			if !validAnswers[in.Answer] {
				fail(w, http.StatusBadRequest, report.Errorf(report.ControlBadRequest, "the answer must be yes, no, always or close, not %q", in.Answer))
				return
			}
			if err := f(r.Context(), r.PathValue("id"), in.Answer); err != nil {
				failAction(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	post("/v1/passthroughs/requests/{id}/answer", answer(b.AnswerPassthrough))
	post("/v1/passthroughs/{id}/close", noContent(func(r *http.Request) error {
		return b.ClosePassthrough(r.Context(), r.PathValue("id"))
	}))
	post("/v1/documents/requests/{id}/answer", answer(b.AnswerDocument))
	post("/v1/documents/{id}/open", noContent(func(r *http.Request) error {
		return b.OpenDocument(r.PathValue("id"))
	}))
	post("/v1/documents/{id}/remove", noContent(func(r *http.Request) error {
		return b.RemoveDocuments(r.PathValue("id"))
	}))
	post("/v1/grants/revoke", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Server    string `json:"server"`
			Requester string `json:"requester"`
			Kind      string `json:"kind"`
		}
		if !body(w, r, &in) {
			return
		}
		if err := b.RevokeGrant(in.Server, in.Requester, in.Kind); err != nil {
			failAction(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/captured/{id}", func(w http.ResponseWriter, r *http.Request) {
		ex, ok := b.Captured(r.PathValue("id"))
		if !ok {
			fail(w, http.StatusNotFound, report.Errorf(report.ControlNotFound, "that request is no longer kept"))
			return
		}
		reply(w, http.StatusOK, ex)
	})
	post("/v1/captured/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		status, err := b.Replay(r.Context(), r.PathValue("id"))
		if err != nil {
			failAction(w, err)
			return
		}
		reply(w, http.StatusOK, struct {
			Status int `json:"status"`
		}{status})
	})
	mux.HandleFunc("GET /v1/environment", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), environmentTimeout)
		defer cancel()
		checks, err := b.Environment(ctx)
		if err != nil {
			failAction(w, err)
			return
		}
		if checks == nil {
			checks = []EnvCheck{}
		}
		reply(w, http.StatusOK, checks)
	})
	post("/v1/pair", func(w http.ResponseWriter, r *http.Request) {
		info, err := b.Pair()
		if err != nil {
			failAction(w, err)
			return
		}
		reply(w, http.StatusCreated, info)
	})
	mux.HandleFunc("GET /v1/pair/{code}", func(w http.ResponseWriter, r *http.Request) {
		st, err := b.PairStatus(r.PathValue("code"))
		if err != nil {
			failAction(w, err)
			return
		}
		reply(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		list, err := b.Devices()
		if err != nil {
			failAction(w, err)
			return
		}
		if list == nil {
			list = []DeviceInfo{}
		}
		reply(w, http.StatusOK, list)
	})
	mux.HandleFunc("DELETE /v1/devices/{name}", noContent(func(r *http.Request) error {
		return b.RevokeDevice(r.PathValue("name"))
	}))
	post("/v1/network/firewall/recheck", noContent(func(r *http.Request) error {
		return b.RecheckFirewall()
	}))
	mux.HandleFunc("GET /v1/page", func(w http.ResponseWriter, r *http.Request) {
		u, err := b.PageKeyURL()
		if err != nil {
			failAction(w, err)
			return
		}
		reply(w, http.StatusOK, struct {
			URL string `json:"url"`
		}{u})
	})
}

// failAction answers with err as problem details. The core's own marks keep
// the statuses they have always had: 501 for ErrUnimplemented, 409 and
// "ended" for ErrRequestEnded, 404 for ErrNotFound, and "allowNotSaved" for
// ErrAllowNotSaved. Any other error answers with its code's status, 500
// for one without. An error that is not a message is sent as one, its text
// the title, with the code of its mark when it has one.
func failAction(w http.ResponseWriter, err error) {
	m := report.From(err)
	plain := m.Code == report.Unexpected
	ext := map[string]any{}
	if errors.Is(err, ErrAllowNotSaved) {
		ext["allowNotSaved"] = true
		if plain {
			m.Code = report.AgentsAllowNotSaved
		}
	}
	status := 0
	switch {
	case errors.Is(err, ErrUnimplemented):
		status = http.StatusNotImplemented
		if plain {
			m.Code = report.ControlUnimplemented
		}
	case errors.Is(err, ErrRequestEnded):
		status, ext["ended"] = http.StatusConflict, true
		if plain {
			m.Code = report.ControlRequestEnded
		}
	case errors.Is(err, ErrNetworkOff):
		status = http.StatusConflict
		if plain {
			m.Code = report.NetworkOff
		}
	case errors.Is(err, ErrDeviceUnknown):
		status = http.StatusNotFound
		if plain {
			m.Code = report.DeviceUnknown
		}
	case errors.Is(err, ErrNotFound):
		// The client tells the core's not found by this code.
		status, m.Code = http.StatusNotFound, report.ControlNotFound
	}
	if status == 0 {
		status = http.StatusInternalServerError
		if e, ok := report.Lookup(m.Code); ok && e.Status >= 400 && e.Status <= 599 {
			status = e.Status
		}
	}
	report.WriteProblemExt(w, m, status, ext, nil)
}

// answerFailed marks a message from the core as ErrRequestEnded when ended
// is set and as ErrAllowNotSaved when notSaved is. It is the message's
// cause, and reads as its title, so a renderer does not show it again.
type answerFailed struct {
	msg             string
	ended, notSaved bool
}

func (e *answerFailed) Error() string { return e.msg }
func (e *answerFailed) Is(target error) bool {
	return (target == ErrRequestEnded && e.ended) || (target == ErrAllowNotSaved && e.notSaved)
}

// notFoundError likewise marks a message as ErrNotFound.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

// unimplementedError likewise marks a message as ErrUnimplemented.
type unimplementedError struct{ msg string }

func (e *unimplementedError) Error() string        { return e.msg }
func (e *unimplementedError) Is(target error) bool { return target == ErrUnimplemented }

// decodeProblem returns the message in a failed request's body: problem
// details, or the {"error": …} of a core from before them, read as an
// error with the code Unexpected. It returns nil for a body that holds
// neither, such as an empty one.
func decodeProblem(body []byte) *report.Message {
	var head struct {
		Error string          `json:"error"`
		Title string          `json:"title"`
		Level json.RawMessage `json:"level"`
	}
	if json.Unmarshal(body, &head) != nil {
		return nil
	}
	var m report.Message
	if json.Unmarshal(body, &m) != nil {
		// A level or a field this client does not know: keep the words.
		m = report.Message{Title: head.Title}
	}
	if m.Title == "" {
		m.Title = head.Error
	}
	if m.Title == "" {
		return nil
	}
	if m.Code == "" {
		m.Code = report.Unexpected
	}
	if len(head.Level) == 0 || m.Level < report.Info || m.Level > report.Error {
		m.Level = report.Error
	}
	return &m
}

// actionError is the error for an answer of code with the body body that an
// action did not want: the core's message, marked as the sentinel the core
// answered for, so errors.Is and report.As both see it.
func actionError(code int, body []byte) error {
	m := decodeProblem(body)
	if m == nil {
		if code == http.StatusNotFound {
			return ErrNotFound
		}
		return report.Errorf(report.Unexpected, "The core answered %d.", code)
	}
	var flags struct {
		Ended         bool `json:"ended"`
		AllowNotSaved bool `json:"allowNotSaved"`
	}
	json.Unmarshal(body, &flags)
	// An older core sends no code, so a message of its is Unexpected.
	plain := m.Code == report.Unexpected
	switch {
	case flags.Ended || flags.AllowNotSaved:
		m.Cause = &answerFailed{msg: m.Title, ended: flags.Ended, notSaved: flags.AllowNotSaved}
	case code == http.StatusNotFound && (plain || m.Code == report.ControlNotFound || m.Code == report.ControlNoRoute):
		m.Cause = &notFoundError{m.Title}
	case code == http.StatusNotImplemented && (plain || m.Code == report.ControlUnimplemented):
		m.Cause = &unimplementedError{m.Title}
	}
	return m
}

// act makes a POST for an action, with in as its JSON body unless that is nil.
// A request that blocks has no cap on its time, only ctx.
func (c *Client) act(ctx context.Context, path string, in, out any, blocks bool) error {
	return c.actMethod(ctx, http.MethodPost, path, in, out, blocks)
}

func (c *Client) actMethod(ctx context.Context, method, path string, in, out any, blocks bool) error {
	var data []byte
	if in != nil {
		var err error
		if data, err = json.Marshal(in); err != nil {
			return err
		}
	}
	code, resp, err := c.send(ctx, method, path, data, !blocks)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return actionError(code, resp)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("the core's answer is not understood: %w", err)
	}
	return nil
}

func esc(s string) string { return url.PathEscape(s) }

// StartProject starts the project and returns once it is up or has failed.
func (c *Client) StartProject(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/projects/"+esc(name)+"/start", nil, nil, true)
}

// StopProject stops the project.
func (c *Client) StopProject(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/projects/"+esc(name)+"/stop", nil, nil, true)
}

// Recheck has the core look at the project again.
func (c *Client) Recheck(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/projects/"+esc(name)+"/recheck", nil, nil, false)
}

// AcceptChange accepts the change to the project's files with the hash sha.
func (c *Client) AcceptChange(ctx context.Context, name, sha string) error {
	return c.act(ctx, "/v1/projects/"+esc(name)+"/change/accept", map[string]string{"sha": sha}, nil, false)
}

// DeclineChange declines the change to the project's files with the hash sha.
func (c *Client) DeclineChange(ctx context.Context, name, sha string) error {
	return c.act(ctx, "/v1/projects/"+esc(name)+"/change/decline", map[string]string{"sha": sha}, nil, false)
}

// ConnectServer connects to the server.
func (c *Client) ConnectServer(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/servers/"+esc(name)+"/connect", nil, nil, false)
}

// DisconnectServer disconnects from the server.
func (c *Client) DisconnectServer(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/servers/"+esc(name)+"/disconnect", nil, nil, false)
}

// ReconnectServer connects to the server again.
func (c *Client) ReconnectServer(ctx context.Context, name string) error {
	return c.act(ctx, "/v1/servers/"+esc(name)+"/reconnect", nil, nil, false)
}

// Reload has the core load its config file and apply it now.
func (c *Client) Reload(ctx context.Context) error {
	return c.act(ctx, "/v1/config/reload", nil, nil, true)
}

// AcceptProposal accepts an agent's proposal, to serve the project as name.
func (c *Client) AcceptProposal(ctx context.Context, id, name string) error {
	return c.act(ctx, "/v1/proposals/"+esc(id)+"/accept", map[string]string{"name": name}, nil, false)
}

// DeclineProposal declines an agent's proposal, saying reason.
func (c *Client) DeclineProposal(ctx context.Context, id, reason string) error {
	return c.act(ctx, "/v1/proposals/"+esc(id)+"/decline", map[string]string{"reason": reason}, nil, false)
}

// AnswerPassthrough answers a passthrough request with yes, no, always or
// close, and returns once the passthrough has opened.
func (c *Client) AnswerPassthrough(ctx context.Context, id, answer string) error {
	return c.act(ctx, "/v1/passthroughs/requests/"+esc(id)+"/answer", map[string]string{"answer": answer}, nil, true)
}

// ClosePassthrough closes an open passthrough.
func (c *Client) ClosePassthrough(ctx context.Context, id string) error {
	return c.act(ctx, "/v1/passthroughs/"+esc(id)+"/close", nil, nil, false)
}

// AnswerDocument answers a document request as AnswerPassthrough does.
func (c *Client) AnswerDocument(ctx context.Context, id, answer string) error {
	return c.act(ctx, "/v1/documents/requests/"+esc(id)+"/answer", map[string]string{"answer": answer}, nil, true)
}

// OpenDocument opens a served document in the browser.
func (c *Client) OpenDocument(ctx context.Context, id string) error {
	return c.act(ctx, "/v1/documents/"+esc(id)+"/open", nil, nil, false)
}

// RemoveDocuments stops serving the documents with the id.
func (c *Client) RemoveDocuments(ctx context.Context, id string) error {
	return c.act(ctx, "/v1/documents/"+esc(id)+"/remove", nil, nil, false)
}

// RevokeGrant takes back a grant that the requester has on the server.
func (c *Client) RevokeGrant(ctx context.Context, server, requester, kind string) error {
	return c.act(ctx, "/v1/grants/revoke", map[string]string{"server": server, "requester": requester, "kind": kind}, nil, false)
}

// Captured returns the captured exchange with the id, and false when the core
// no longer keeps it or cannot be asked.
func (c *Client) Captured(ctx context.Context, id string) (proxy.Exchange, bool) {
	ex, err := c.CapturedExchange(ctx, id)
	return ex, err == nil
}

// CapturedExchange returns the captured exchange with the id, or an error for
// which errors.Is(err, ErrNotFound) when the core no longer keeps it. Any
// other error says the core could not be asked.
func (c *Client) CapturedExchange(ctx context.Context, id string) (proxy.Exchange, error) {
	var ex proxy.Exchange
	if err := c.actMethod(ctx, http.MethodGet, "/v1/captured/"+esc(id), nil, &ex, false); err != nil {
		return proxy.Exchange{}, err
	}
	return ex, nil
}

// Replay sends a captured request again and returns the status it got.
func (c *Client) Replay(ctx context.Context, id string) (int, error) {
	var out struct {
		Status int `json:"status"`
	}
	err := c.act(ctx, "/v1/captured/"+esc(id)+"/replay", nil, &out, true)
	return out.Status, err
}

// PageKeyURL returns the address of the home page with its key.
func (c *Client) PageKeyURL(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.actMethod(ctx, http.MethodGet, "/v1/page", nil, &out, false)
	return out.URL, err
}

// Environment asks the core what it can reach: opening links, the SSH agent,
// Docker and ssh. It returns ErrNoRoute when the core is an older release
// that does not answer this.
func (c *Client) Environment(ctx context.Context) ([]EnvCheck, error) {
	// The core looks when asked and takes up to environmentTimeout, which is
	// longer than a call's cap, so the wait is bounded here.
	ctx, cancel := context.WithTimeout(ctx, environmentTimeout+2*time.Second)
	defer cancel()
	var out []EnvCheck
	err := c.actMethod(ctx, http.MethodGet, "/v1/environment", nil, &out, true)
	if errors.Is(err, ErrNotFound) {
		// Only this route maps a 404 to ErrNoRoute: the core never answers
		// it with one, so a 404 is a core that has no such route.
		return nil, ErrNoRoute
	}
	return out, err
}

// Pair asks the core for a pairing code. It returns an error with the code
// network.off when the core does not serve to the network.
func (c *Client) Pair(ctx context.Context) (PairInfo, error) {
	var out PairInfo
	err := c.act(ctx, "/v1/pair", nil, &out, false)
	return out, err
}

// PairStatus asks what became of a pairing code.
func (c *Client) PairStatus(ctx context.Context, code string) (PairState, error) {
	var out PairState
	err := c.actMethod(ctx, http.MethodGet, "/v1/pair/"+esc(code), nil, &out, false)
	return out, err
}

// Devices lists the paired devices.
func (c *Client) Devices(ctx context.Context) ([]DeviceInfo, error) {
	var out []DeviceInfo
	err := c.actMethod(ctx, http.MethodGet, "/v1/devices", nil, &out, false)
	return out, err
}

// RecheckFirewall asks the core to look for the Windows firewall rule now.
func (c *Client) RecheckFirewall(ctx context.Context) error {
	return c.act(ctx, "/v1/network/firewall/recheck", nil, nil, false)
}

// RevokeDevice unpairs a device.
func (c *Client) RevokeDevice(ctx context.Context, name string) error {
	return c.actMethod(ctx, http.MethodDelete, "/v1/devices/"+esc(name), nil, nil, false)
}
