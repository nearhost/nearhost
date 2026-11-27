package control

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

// goTypes maps each component that describes a Go struct to that struct.
// The schema's property names and required lists must match what the struct
// marshals to.
var goTypes = map[string]reflect.Type{
	"Step":                reflect.TypeFor[report.Step](),
	"Info":                reflect.TypeFor[Info](),
	"Status":              reflect.TypeFor[Status](),
	"ServerInfo":          reflect.TypeFor[ServerInfo](),
	"ProjectInfo":         reflect.TypeFor[ProjectInfo](),
	"ApprovalInfo":        reflect.TypeFor[ApprovalInfo](),
	"NetworkInfo":         reflect.TypeFor[NetworkInfo](),
	"EnvCheck":            reflect.TypeFor[EnvCheck](),
	"State":               reflect.TypeFor[State](),
	"ProjectStatus":       reflect.TypeFor[engine.ProjectStatus](),
	"Change":              reflect.TypeFor[engine.Change](),
	"Project":             reflect.TypeFor[config.Project](),
	"Route":               reflect.TypeFor[config.Route](),
	"RcSnapshot":          reflect.TypeFor[config.RcSnapshot](),
	"Item":                reflect.TypeFor[nearhostrc.Item](),
	"ServerState":         reflect.TypeFor[ServerState](),
	"ServerHealth":        reflect.TypeFor[engine.ServerHealth](),
	"EnginePassthrough":   reflect.TypeFor[engine.Passthrough](),
	"Tree":                reflect.TypeFor[documents.Tree](),
	"Doc":                 reflect.TypeFor[documents.Doc](),
	"Grant":               reflect.TypeFor[grants.Grant](),
	"Envelope":            reflect.TypeFor[Envelope](),
	"EngineEvent":         reflect.TypeFor[engine.Event](),
	"Approval":            reflect.TypeFor[Approval](),
	"Proposal":            reflect.TypeFor[agentapi.Proposal](),
	"PassRequest":         reflect.TypeFor[agentapi.PassRequest](),
	"DocRequest":          reflect.TypeFor[agentapi.DocRequest](),
	"AgentapiPassthrough": reflect.TypeFor[agentapi.Passthrough](),
	"Shown":               reflect.TypeFor[Shown](),
	"Exchange":            reflect.TypeFor[proxy.Exchange](),
	"PairInfo":            reflect.TypeFor[PairInfo](),
	"PairState":           reflect.TypeFor[PairState](),
	"DeviceInfo":          reflect.TypeFor[DeviceInfo](),
	"Session":             reflect.TypeFor[proxy.Session](),
}

// notGoTypes lists the object components that are not one Go struct, and why.
var notGoTypes = map[string]string{
	"Problem":                "the problem body: report.Message's members with type, status and error, written by report.WriteProblem",
	"Message":                "report.Message has its own MarshalJSON, so its fields are not its JSON members",
	"Header":                 "http.Header, a map of string slices, not a struct",
	"PageURL":                "an anonymous struct in the page handler",
	"ReplayResult":           "an anonymous struct in the replay handler",
	"ChangeRequest":          "a request body the handler decodes into an anonymous struct",
	"AcceptProposalRequest":  "a request body the handler decodes into an anonymous struct",
	"DeclineProposalRequest": "a request body the handler decodes into an anonymous struct",
	"AnswerRequest":          "a request body the handler decodes into an anonymous struct",
	"RevokeGrantRequest":     "a request body the handler decodes into an anonymous struct",
}

// jsonField is one member a struct marshals to.
type jsonField struct {
	name      string
	omitEmpty bool
	typ       reflect.Type
}

// jsonFields returns the members t marshals to, with embedded structs
// flattened as encoding/json does.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for f := range t.Fields() {
		tag, hasTag := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, jsonFields(et)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if !hasTag || name == "" {
			name = f.Name
		}
		out = append(out, jsonField{name, slices.Contains(strings.Split(opts, ","), "omitempty"), f.Type})
	}
	return out
}

// allowsNull reports whether a property accepts null: its type lists it, or
// one of its anyOf alternatives does.
func allowsNull(s *openapi3.Schema) bool {
	if s.Type != nil && s.Type.Includes("null") {
		return true
	}
	for _, alt := range s.AnyOf {
		if alt.Value != nil && allowsNull(alt.Value) {
			return true
		}
	}
	return false
}

// isDateTime reports whether a property is a date-time string.
func isDateTime(s *openapi3.Schema) bool {
	return s.Type != nil && s.Type.Includes("string") && s.Format == "date-time"
}

// compareFields returns how the schema s differs from what t marshals to.
func compareFields(t reflect.Type, s *openapi3.Schema) []string {
	var bad []string
	fields := jsonFields(t)
	want := map[string]bool{}
	for _, f := range fields {
		want[f.name] = true
		ref := s.Properties[f.name]
		if ref == nil || ref.Value == nil {
			bad = append(bad, fmt.Sprintf("property %q is missing", f.name))
			continue
		}
		prop := ref.Value
		required := slices.Contains(s.Required, f.name)
		isPtr := f.typ.Kind() == reflect.Pointer
		if wantReq := !f.omitEmpty && !isPtr; required != wantReq {
			bad = append(bad, fmt.Sprintf("property %q: required = %v, want %v", f.name, required, wantReq))
		}
		nilable := f.typ.Kind() == reflect.Slice || f.typ.Kind() == reflect.Map || isPtr
		if nilable && !f.omitEmpty && !allowsNull(prop) {
			bad = append(bad, fmt.Sprintf("property %q is a nil-able %s without omitempty, so it must allow null", f.name, f.typ.Kind()))
		}
		if f.typ == reflect.TypeFor[time.Time]() && !isDateTime(prop) {
			bad = append(bad, fmt.Sprintf("property %q is a time.Time, so it must be a string with format date-time", f.name))
		}
	}
	for name := range s.Properties {
		if !want[name] {
			bad = append(bad, fmt.Sprintf("property %q has no Go field", name))
		}
	}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			bad = append(bad, fmt.Sprintf("required %q is not a property", r))
		}
	}
	slices.Sort(bad)
	return bad
}

// Each schema component that describes a Go struct has the struct's members,
// required as the struct says.
func TestComponentsMatchGoTypes(t *testing.T) {
	doc := loadSchema(t)
	for name, typ := range goTypes {
		ref := doc.Components.Schemas[name]
		if ref == nil || ref.Value == nil {
			t.Errorf("%s: no such component", name)
			continue
		}
		for _, problem := range compareFields(typ, ref.Value) {
			t.Errorf("%s (%s): %s", name, typ, problem)
		}
	}
}

// Every object component is a Go type in goTypes or is listed in notGoTypes.
func TestEveryObjectComponentIsAccountedFor(t *testing.T) {
	for name, ref := range loadSchema(t).Components.Schemas {
		if ref.Value == nil || ref.Value.Type == nil || !ref.Value.Type.Includes("object") {
			continue
		}
		_, isGo := goTypes[name]
		_, isNot := notGoTypes[name]
		switch {
		case isGo && isNot:
			t.Errorf("%s is in both goTypes and notGoTypes", name)
		case !isGo && !isNot:
			t.Errorf("%s is an object component in neither goTypes nor notGoTypes", name)
		}
	}
	doc := loadSchema(t)
	for name := range goTypes {
		if doc.Components.Schemas[name] == nil {
			t.Errorf("goTypes names %s, which is not a component", name)
		}
	}
	for name := range notGoTypes {
		if doc.Components.Schemas[name] == nil {
			t.Errorf("notGoTypes names %s, which is not a component", name)
		}
	}
}

// The walker reports a Go field the schema lacks, a property the Go type
// lacks, and a wrong required list, so the test above can fail.
func TestFieldWalkerReportsMismatches(t *testing.T) {
	type embedded struct {
		Inner string `json:"inner"`
	}
	type fixture struct {
		embedded
		Name    string            `json:"name"`
		Note    string            `json:"note,omitempty"`
		Skipped string            `json:"-"`
		Tags    []string          `json:"tags"`
		Extra   map[string]string `json:"extra"`
		At      time.Time         `json:"at"`
		hidden  int
	}
	_ = fixture{}.hidden
	const spec = `
openapi: 3.1.0
info: {title: t, version: "1"}
paths: {}
components:
  schemas:
    Fixture:
      type: object
      required: [inner, name, note, tags, at]
      properties:
        inner: {type: string}
        name: {type: string}
        note: {type: string}
        tags: {type: array, items: {type: string}}
        at: {type: string}
        stale: {type: string}
`
	doc, err := openapi3.NewLoader().LoadFromData([]byte(spec))
	if err != nil {
		t.Fatalf("load the fixture: %v", err)
	}
	got := strings.Join(compareFields(reflect.TypeFor[fixture](), doc.Components.Schemas["Fixture"].Value), "\n")
	for _, want := range []string{
		`property "extra" is missing`,
		`property "stale" has no Go field`,
		`property "note": required = true, want false`,
		`property "tags" is a nil-able slice without omitempty, so it must allow null`,
		`property "at" is a time.Time`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the walker did not report %q; it reported:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{`"inner"`, `"skipped"`, `"Skipped"`, `"hidden"`, `property "name"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the walker reported %s, which is right; it reported:\n%s", unwanted, got)
		}
	}
}
