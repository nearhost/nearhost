package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// nonWireMessageFields are the report.Message fields MarshalJSON never
// sends. Add a field here only when it is not part of the JSON body.
var nonWireMessageFields = []string{"Cause", "Exit"}

// fillNonZero sets v, and what it holds, to a non-zero value.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(t, s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		if v.Type().Key().Kind() != reflect.String {
			t.Fatalf("cannot fill a map keyed by %s", v.Type().Key())
		}
		val := reflect.New(v.Type().Elem()).Elem()
		if val.Kind() == reflect.Interface {
			val.Set(reflect.ValueOf(1))
		} else {
			fillNonZero(t, val)
		}
		m.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), val)
		v.Set(m)
	case reflect.Struct:
		for f := range v.Type().Fields() {
			if f.IsExported() {
				fillNonZero(t, v.Field(f.Index[0]))
			}
		}
	default:
		t.Fatalf("fillNonZero does not handle %s; add it", v.Type())
	}
}

// fullMessage is a message with every wire field set.
func fullMessage(t *testing.T) *report.Message {
	t.Helper()
	m := &report.Message{}
	v := reflect.ValueOf(m).Elem()
	for f := range v.Type().Fields() {
		if !f.IsExported() || slices.Contains(nonWireMessageFields, f.Name) {
			continue
		}
		fillNonZero(t, v.FieldByName(f.Name))
	}
	// Level 0 is info and is never "non-zero"; the walk set it to 1, warning.
	return m
}

func keysOf(t *testing.T, body []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("the body is not a JSON object: %v\n%s", err, body)
	}
	return slices.Sorted(maps.Keys(obj))
}

func propertyNames(s *openapi3.Schema) []string {
	return slices.Sorted(maps.Keys(s.Properties))
}

func requireSubset(t *testing.T, name string, s *openapi3.Schema) {
	t.Helper()
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			t.Errorf("%s: required %q is not a property", name, r)
		}
	}
}

// The Message component has exactly the members report.Message marshals
// with every field set, and its required members are among them.
func TestMessageComponentMatchesItsJSON(t *testing.T) {
	schema := loadSchema(t).Components.Schemas["Message"].Value
	m := fullMessage(t)
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got := keysOf(t, body)
	if want := propertyNames(schema); !slices.Equal(got, want) {
		t.Errorf("Message marshals to %v, the schema has %v", got, want)
	}
	for _, r := range schema.Required {
		if !slices.Contains(got, r) {
			t.Errorf("required %q is not a member of a marshalled message", r)
		}
	}
	// A bare message has only the required members.
	bare, _ := json.Marshal(&report.Message{})
	if got, want := keysOf(t, bare), slices.Sorted(slices.Values(schema.Required)); !slices.Equal(got, want) {
		t.Errorf("an empty message marshals to %v, the schema requires %v", got, want)
	}
	// Every field that is not listed as non-wire changes the body, so a new
	// field the JSON does not carry (or one this test cannot fill) is caught.
	v := reflect.ValueOf(m).Elem()
	for f := range v.Type().Fields() {
		if !f.IsExported() {
			continue
		}
		c := *m
		cv := reflect.ValueOf(&c).Elem().FieldByName(f.Name)
		cv.Set(reflect.Zero(cv.Type()))
		b, _ := json.Marshal(&c)
		differs := string(b) != string(body)
		if nonWire := slices.Contains(nonWireMessageFields, f.Name); differs == nonWire {
			t.Errorf("Message.%s: changes the JSON = %v, but listed in nonWireMessageFields = %v", f.Name, differs, nonWire)
		}
	}
}

// The Problem component has exactly the members of a problem body written
// with the control API's extensions and a warning.
func TestProblemComponentMatchesItsJSON(t *testing.T) {
	schema := loadSchema(t).Components.Schemas["Problem"].Value
	m := fullMessage(t)
	m.Level = report.Error
	m.Code = report.Unexpected
	rec := httptest.NewRecorder()
	report.WriteProblemExt(rec, m, 409, map[string]any{"ended": true, "allowNotSaved": true}, []*report.Message{fullMessage(t)})
	got := keysOf(t, rec.Body.Bytes())
	if want := propertyNames(schema); !slices.Equal(got, want) {
		t.Errorf("a problem body has %v, the schema has %v", got, want)
	}
	for _, r := range schema.Required {
		if !slices.Contains(got, r) {
			t.Errorf("required %q is not a member of a problem body", r)
		}
	}
	requireSubset(t, "Problem", schema)

	// The real handler path sends the same extensions.
	for _, tc := range []struct {
		err error
		key string
	}{{ErrRequestEnded, "ended"}, {ErrAllowNotSaved, "allowNotSaved"}} {
		rec := httptest.NewRecorder()
		failAction(rec, fmt.Errorf("x: %w", tc.err))
		keys := keysOf(t, rec.Body.Bytes())
		if !slices.Contains(keys, tc.key) {
			t.Errorf("failAction(%v) has no %q: %v", errors.Unwrap(fmt.Errorf("%w", tc.err)), tc.key, keys)
		}
		for _, k := range keys {
			if !slices.Contains(propertyNames(schema), k) {
				t.Errorf("failAction(%v) sends %q, which the Problem schema lacks", tc.err, k)
			}
		}
	}
}

// enumOf returns a string component's enum values.
func enumOf(t *testing.T, name string) []string {
	t.Helper()
	ref := loadSchema(t).Components.Schemas[name]
	if ref == nil || ref.Value == nil {
		t.Fatalf("no component %s", name)
	}
	var out []string
	for _, v := range ref.Value.Enum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s: enum value %v is not a string", name, v)
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Each enum component lists exactly the Go constants of its type. The Go
// constants are listed here: add a new constant to its list.
func TestEnumsMatchGoConstants(t *testing.T) {
	levels := []string{}
	for l := report.Info; ; l++ {
		if _, err := l.MarshalText(); err != nil {
			break
		}
		levels = append(levels, l.String())
	}
	for name, consts := range map[string][]string{
		"StartedBy": {string(StartedService), string(StartedDetached), string(StartedTUI), string(StartedForeground)},
		// Add a new Kind* constant (control/state.go) here.
		"EnvelopeKind": {KindEvent, KindApproval, KindEnded, KindOpened, KindShown, KindReset, KindConfig},
		// Add a new Status constant (transport/session.go) here.
		"TransportStatus": {string(transport.StatusDisconnected), string(transport.StatusConnecting), string(transport.StatusConnected),
			string(transport.StatusReconnecting), string(transport.StatusAuthFailed)},
		// Add a new State constant (engine/engine.go) here.
		"EngineState": {string(engine.StateStopped), string(engine.StateStarting), string(engine.StateRunning), string(engine.StateError), string(engine.StateDegraded)},
		// Levels are counted from report.Info while a level marshals.
		"Level": levels,
	} {
		want := slices.Clone(consts)
		slices.Sort(want)
		if got := enumOf(t, name); !slices.Equal(got, want) {
			t.Errorf("%s: the schema enum is %v, the Go constants are %v", name, got, want)
		}
	}
}
