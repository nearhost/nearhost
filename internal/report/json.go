package report

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// wire is a message as JSON. A nested message (in a `warnings` list, an
// event or the state) has no `error` key: only a top-level error body
// (WriteProblem) has one.
type wire struct {
	Code   Code           `json:"code,omitempty"`
	Level  Level          `json:"level"`
	Title  string         `json:"title,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Fix    []Step         `json:"fix,omitempty"`
	Doctor bool           `json:"doctor,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// MarshalJSON writes the message as
// {"code","level","title","detail","fix":[{"text","command"}],"doctor","data"},
// leaving out empty fields. The cause, the exit status and the context are
// not sent. JSON escaping keeps any text in it inert.
func (m *Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(wire{
		Code: m.Code, Level: m.Level, Title: m.Title, Detail: m.Why,
		Fix: m.Fix, Doctor: m.Doctor, Data: m.Data,
	})
}

// UnmarshalJSON reads what MarshalJSON writes, and a problem details body,
// whose extra keys (type, status, error, warnings) it ignores.
func (m *Message) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*m = Message{
		Level: w.Level, Code: w.Code, Title: w.Title, Why: w.Detail,
		Fix: w.Fix, Doctor: w.Doctor, Data: w.Data,
	}
	return nil
}

// Problem returns an error as RFC 9457 problem details: the message's
// fields, plus `type` (typeBase + "#" + code), `status`, and `error`, which
// holds the title for clients that read only that.
func Problem(m *Message, status int, typeBase string) map[string]any {
	p := map[string]any{
		"type":   typeBase + "#" + string(m.Code),
		"title":  m.Title,
		"status": status,
		"code":   m.Code,
		"level":  m.Level.String(),
		"error":  m.Title,
	}
	if m.Why != "" {
		p["detail"] = m.Why
	}
	if len(m.Fix) > 0 {
		p["fix"] = m.Fix
	}
	if m.Doctor {
		p["doctor"] = true
	}
	if len(m.Data) > 0 {
		p["data"] = m.Data
	}
	return p
}

// problemOrder is the order of a problem body's keys. `error` comes first,
// as the agent skill takes the first "error" in a body; keys a body lacks
// are skipped.
var problemOrder = []string{"error", "type", "title", "status", "detail", "code", "level", "fix", "doctor", "data"}

// WriteProblem answers a request with m as problem details
// (application/problem+json), with the HTTP status from m's catalog entry
// (500 for a code without one), and warnings, if any, last. A caller that
// needs a header, such as Retry-After, sets it first.
func WriteProblem(w http.ResponseWriter, m *Message, warnings []*Message) {
	status := http.StatusInternalServerError
	if m != nil {
		if e, ok := Lookup(m.Code); ok && e.Status >= 400 && e.Status <= 599 {
			status = e.Status
		}
	}
	WriteProblemStatus(w, m, status, warnings)
}

// WriteProblemStatus is WriteProblem with the HTTP status given, for an
// API that must keep the status it has always answered with.
func WriteProblemStatus(w http.ResponseWriter, m *Message, status int, warnings []*Message) {
	if m == nil {
		m = Errorf(Unexpected, "Something went wrong.")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	w.Write(safeProblemBody(m, status, warnings))
}

// safeProblemBody returns m and warnings as a problem body that always has
// m's title under `error`. A value in some Data that does not marshal, or a
// warning with a level outside the three, would fail the body, so it is
// tried again without any message's data, then without the warnings, and
// at last as the error and title alone.
func safeProblemBody(m *Message, status int, warnings []*Message) []byte {
	if body, err := problemBody(Problem(m, status, DocsBase), warnings); err == nil {
		return body
	}
	bare := *m
	bare.Data = nil
	var plain []*Message
	for _, wm := range warnings {
		if wm == nil {
			continue
		}
		c := *wm
		c.Data = nil
		plain = append(plain, &c)
	}
	if body, err := problemBody(Problem(&bare, status, DocsBase), plain); err == nil {
		return body
	}
	if body, err := problemBody(Problem(&bare, status, DocsBase), nil); err == nil {
		return body
	}
	title, _ := json.Marshal(m.Title)
	return []byte(`{"error":` + string(title) + `,"title":` + string(title) + "}\n")
}

// problemBody writes p's keys in problemOrder, then warnings.
func problemBody(p map[string]any, warnings []*Message) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	field := func(k string, v any) error {
		val, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
		return nil
	}
	for _, k := range problemOrder {
		v, ok := p[k]
		if !ok {
			continue
		}
		if err := field(k, v); err != nil {
			return nil, err
		}
	}
	if len(warnings) > 0 {
		if err := field("warnings", warnings); err != nil {
			return nil, err
		}
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}
