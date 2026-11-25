package agentapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"

	"github.com/nearhost/nearhost/internal/report"
)

// The agent API answers errors as problem details (report.WriteProblem),
// whose `error` is the text agents have always been told, and every reply
// with the warnings reported while it was worked out. The skill already on
// servers reads these replies with sh: it takes the first "error" in a body
// as the error, and the last "state" (and the other names in skillNames) as
// the reply's own, relying on Go writing object keys sorted. So `error`
// comes first in an error, warnings come last, and no message an agent is
// sent has data under a name the skill reads.

// skillNames are the names the skill's scripts read from a reply. Data
// under them is left out of every message an agent is sent, so a nested one
// is never read as the reply's own.
var skillNames = []string{"error", "state", "api", "laptop", "held", "retry_after", "approve_url", "advice", "reason", "id", "token"}

// forAgents is m as an agent is sent it: a copy whose data, at any depth,
// has nothing under a name in skillNames. m is left as it is. It is nil
// when m is.
func forAgents(m *report.Message) *report.Message {
	if m == nil {
		return nil
	}
	c := *m
	c.Fix, c.Verbatim = slices.Clone(m.Fix), slices.Clone(m.Verbatim)
	if len(m.Data) > 0 {
		c.Data, _ = withoutSkillNames(m.Data).(map[string]any)
		if len(c.Data) == 0 {
			c.Data = nil
		}
	}
	return &c
}

// withoutSkillNames is v with every object in it copied without the keys
// in skillNames.
func withoutSkillNames(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if !slices.Contains(skillNames, k) {
				out[k] = withoutSkillNames(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = withoutSkillNames(e)
		}
		return out
	}
	return v
}

// MarshalJSON writes the project with its problem as an agent is sent it
// (see forAgents).
func (p Project) MarshalJSON() ([]byte, error) {
	type plain Project
	q := plain(p)
	q.Problem = forAgents(p.Problem)
	return json.Marshal(q)
}

// warningsWriter is a reply being written, with the warnings reported while
// its request is handled.
type warningsWriter struct {
	http.ResponseWriter
	c *report.Collector
}

// collecting serves next with a sink in each request's context that keeps
// what is reported, for its reply, and passes it on to report.Fallback, the
// core's log, as before.
func collecting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &report.Collector{}
		log := report.SinkFunc(func(m *report.Message) {
			if f := report.Fallback; f != nil {
				f.Report(m)
			}
		})
		ctx := report.WithSink(r.Context(), report.Fan(c, log))
		next.ServeHTTP(&warningsWriter{w, c}, r.WithContext(ctx))
	})
}

// warnings are the warnings reported so far for the reply w writes, as
// agents are sent them.
func warnings(w http.ResponseWriter) []*report.Message {
	ww, ok := w.(*warningsWriter)
	if !ok {
		return nil
	}
	var out []*report.Message
	for _, m := range ww.c.Messages() {
		if m != nil {
			out = append(out, forAgents(m))
		}
	}
	return out
}

// reply answers with v, an object, as JSON, with the warnings reported
// while it was worked out last, under "warnings", when there are any. A v
// that does not marshal answers 500 with why.
func reply(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		fail(w, http.StatusInternalServerError, report.Errorf(report.Unexpected, "the reply could not be written as JSON: %s", err).Wrap(err))
		return
	}
	body = withWarnings(body, warnings(w))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}

// withWarnings is body, a JSON object, with ws added as its last key,
// "warnings". A warning whose data does not marshal goes without its data;
// body is left as it is when there are none, or it is not an object.
func withWarnings(body []byte, ws []*report.Message) []byte {
	if len(ws) == 0 || len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return body
	}
	list, err := json.Marshal(ws)
	if err != nil {
		plain := make([]*report.Message, len(ws))
		for i, m := range ws {
			c := *m
			c.Data = nil
			plain[i] = &c
		}
		if list, err = json.Marshal(plain); err != nil {
			return body
		}
	}
	out := slices.Clone(body[:len(body)-1])
	if len(body) > 2 {
		out = append(out, ',')
	}
	out = append(out, `"warnings":`...)
	out = append(out, list...)
	return append(out, '}')
}

// fail answers with m as problem details, with status, the status that
// error has always had, and the warnings reported so far.
func fail(w http.ResponseWriter, status int, m *report.Message) {
	failWith(w, status, m, nil)
}

// failWith is fail with ext, keys the error has always had beside its
// `error`, such as a reload's items.
func failWith(w http.ResponseWriter, status int, m *report.Message, ext map[string]any) {
	ext = maps.Clone(ext)
	for _, k := range skillNames {
		delete(ext, k)
	}
	report.WriteProblemExt(w, forAgents(m), status, ext, warnings(w))
}

// failed is err as the error an agent is told: its text in full as the
// title, as agents have always been told it. A message in err's chain
// gives its code, fix and data; any other error gets code.
func failed(code report.Code, err error) *report.Message {
	if m, ok := report.As(err); ok {
		c := *m
		c.Fix, c.Verbatim, c.Data = slices.Clone(m.Fix), slices.Clone(m.Verbatim), maps.Clone(m.Data)
		c.Level, c.Title, c.Why, c.Context, c.Cause = report.Error, err.Error(), "", "", err
		return &c
	}
	return report.Errorf(code, "%s", err).Wrap(err)
}

// badRequester is the error for a requester that is not a plain name.
func badRequester(requester string) *report.Message {
	return report.Errorf(report.AgentBadRequest, "requester must be 1 to 32 lowercase letters, digits and -, such as claude-code, not %q", requester)
}

// badSession is the error for a session id that is too long or not
// printable.
func badSession() *report.Message {
	return report.Errorf(report.AgentBadRequest, "session must be at most 128 printable characters")
}

// badLabel is the error for a label that could not be shown on one line.
func badLabel() *report.Message {
	return report.Errorf(report.AgentBadRequest, "label must be at most 64 printable characters on one line")
}

// revokedToken is the error for a token the grants do not hold for
// requester on origin.
func revokedToken(requester, origin string) *report.Message {
	return report.Errorf(report.AgentTokenRevoked, "the token for %s on %s was revoked or never existed; drop it and ask again without it", requester, origin)
}
