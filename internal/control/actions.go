package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/nearhost/nearhost/internal/proxy"
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
}

// UnimplementedActions is an Actions that can do none of them: each action
// that returns an error returns ErrUnimplemented. Embed it in a Backend that
// does not do them yet.
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

// maxActionBody is the longest request body an action route reads.
const maxActionBody = 64 << 10

// actionErrorBody is the JSON body of an action that failed. Ended is set when
// the request to answer had ended, and AllowNotSaved when the answer's Always
// allow could not be saved; both may be.
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
			fail(w, http.StatusBadRequest, "the body is not the JSON this route takes: "+err.Error())
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
				fail(w, http.StatusBadRequest, fmt.Sprintf("the answer must be yes, no, always or close, not %q", in.Answer))
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
			fail(w, http.StatusNotFound, "that request is no longer kept")
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

// failAction answers with the status that says why an action failed.
func failAction(w http.ResponseWriter, err error) {
	body := actionErrorBody{Error: err.Error(), AllowNotSaved: errors.Is(err, ErrAllowNotSaved)}
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrUnimplemented):
		code = http.StatusNotImplemented
	case errors.Is(err, ErrRequestEnded):
		code, body.Ended = http.StatusConflict, true
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	}
	reply(w, code, body)
}

// answerFailed is an error that reads as the core's text, and is
// ErrRequestEnded when ended is set and ErrAllowNotSaved when notSaved is.
type answerFailed struct {
	msg             string
	ended, notSaved bool
}

func (e *answerFailed) Error() string { return e.msg }
func (e *answerFailed) Is(target error) bool {
	return (target == ErrRequestEnded && e.ended) || (target == ErrAllowNotSaved && e.notSaved)
}

// notFoundError is likewise ErrNotFound.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

// actionError is the error for an answer of code with the body body that an
// action did not want.
func actionError(code int, body []byte) error {
	var e actionErrorBody
	if json.Unmarshal(body, &e) != nil || e.Error == "" {
		if code == http.StatusNotFound {
			return ErrNotFound
		}
		return fmt.Errorf("the core answered %d", code)
	}
	switch {
	case e.Ended || e.AllowNotSaved:
		return &answerFailed{msg: e.Error, ended: e.Ended, notSaved: e.AllowNotSaved}
	case code == http.StatusNotFound:
		return &notFoundError{e.Error}
	}
	return errors.New(e.Error)
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
