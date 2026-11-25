package report

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
)

func setcap() *Message {
	return Errorf(PortNeedsPermission, "HTTPS port %d needs permission to bind.", 443).
		Because("On Linux …").
		Step("To give it permission, run:", "sudo setcap cap_net_bind_service=+ep '/p/nearhost'").
		SuggestDoctor()
}

func TestErrorReadsAsTitleThenWhy(t *testing.T) {
	m := Errorf(PortNeedsPermission, "HTTPS port %d needs permission to bind.", 443).Because("On Linux …")
	if got, want := m.Error(), "HTTPS port 443 needs permission to bind.\n\nOn Linux …"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if got := Errorf(PortInUse, "Port 80 is in use.").Error(); got != "Port 80 is in use." {
		t.Fatalf("Error() without Why = %q", got)
	}
}

func TestBuildersSetFields(t *testing.T) {
	cause := errors.New("bind: permission denied")
	m := Errorf(PortNeedsPermission, "t").
		Because("why %d", 1).
		Step("run:", "cmd").
		Then("then.").
		SuggestDoctor().
		With("port", 443).
		Wrap(cause).
		ExitCode(3)
	if m.Level != Error || m.Code != PortNeedsPermission || m.Why != "why 1" || !m.Doctor ||
		m.Data["port"] != 443 || m.Cause != cause || m.Exit != 3 {
		t.Fatalf("builder fields wrong: %+v", m)
	}
	want := []Step{{Text: "run:", Command: "cmd"}, {Text: "then."}}
	if !slices.Equal(m.Fix, want) {
		t.Fatalf("Fix = %+v, want %+v", m.Fix, want)
	}
	if Warnf(HostsPublic, "w").Level != Warning || Notef(PresetsNote, "n").Level != Info {
		t.Fatal("Warnf or Notef set the wrong level")
	}
	if got := Errorf(PortInUse, "a").Because("one").Because("two").Why; got != "one\n\ntwo" {
		t.Fatalf("a second Because = %q, want a new paragraph", got)
	}
}

func TestLevelString(t *testing.T) {
	for l, want := range map[Level]string{Info: "info", Warning: "warning", Error: "error"} {
		if l.String() != want {
			t.Errorf("%d.String() = %q, want %q", l, l.String(), want)
		}
	}
}

func TestUnwrapKeepsCause(t *testing.T) {
	m := Errorf(PortNeedsPermission, "x").Wrap(fs.ErrPermission)
	if !errors.Is(m, fs.ErrPermission) {
		t.Fatal("errors.Is does not see the cause")
	}
	wrapped := fmt.Errorf("start web: %w", m)
	if !errors.Is(wrapped, fs.ErrPermission) {
		t.Fatal("errors.Is does not see the cause through a wrapper")
	}
}

func TestAsFindsMessage(t *testing.T) {
	m := setcap()
	got, ok := As(fmt.Errorf("a: %w", fmt.Errorf("b: %w", m)))
	if !ok || got != m {
		t.Fatalf("As = %v, %v", got, ok)
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("As found a message in a plain error")
	}
	if _, ok := As(nil); ok {
		t.Fatal("As found a message in nil")
	}
}

func TestFromFindsWrappedMessage(t *testing.T) {
	m := setcap()
	got := From(fmt.Errorf("start web: %w", m))
	if got.Code != PortNeedsPermission {
		t.Fatalf("code = %q", got.Code)
	}
	if !slices.Equal(got.Fix, m.Fix) {
		t.Fatalf("Fix = %+v, want %+v", got.Fix, m.Fix)
	}
	if got.Context != "start web" {
		t.Fatalf("Context = %q, want %q", got.Context, "start web")
	}
	if got == m {
		t.Fatal("From returned the message itself, not a copy")
	}
	if m.Context != "" {
		t.Fatal("From changed the original message")
	}
	if got.Title != m.Title || !got.Doctor {
		t.Fatalf("From lost fields: %+v", got)
	}
}

func TestFromNestedContext(t *testing.T) {
	m := setcap()
	got := From(fmt.Errorf("up: %w", fmt.Errorf("start web: %w", m)))
	if got.Context != "up: start web" {
		t.Fatalf("Context = %q", got.Context)
	}
	if c := From(m).Context; c != "" {
		t.Fatalf("From of the message itself has Context %q", c)
	}
}

func TestFromWrapsPlainError(t *testing.T) {
	e := errors.New("boom")
	got := From(e)
	if got.Code != Unexpected || got.Title != "boom" || got.Level != Error {
		t.Fatalf("From(plain) = %+v", got)
	}
	if !errors.Is(got, e) {
		t.Fatal("From(plain) does not unwrap to the error")
	}
	if From(nil) != nil {
		t.Fatal("From(nil) is not nil")
	}
}
