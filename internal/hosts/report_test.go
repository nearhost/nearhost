package hosts

import (
	"errors"
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// A read-only hosts file is hosts.read-only, with how to clear it and how
// to serve without it as steps with commands, while the error's text stays as it was.
func TestReadOnlyIsAMessage(t *testing.T) {
	err := error(&readOnlyError{path: `C:\Windows\System32\drivers\etc\hosts`})
	m, ok := report.As(err)
	if !ok || m.Code != report.HostsReadOnly {
		t.Fatalf("message = %+v, want %q", m, report.HostsReadOnly)
	}
	if m.Title != `C:\Windows\System32\drivers\etc\hosts is read-only.` {
		t.Errorf("title = %q", m.Title)
	}
	want := []report.Step{
		{Text: "Clear the read-only attribute in an administrator terminal:", Command: `attrib -r "C:\Windows\System32\drivers\etc\hosts"`},
		{Text: "Or serve without updating it:", Command: "nearhost --no-hosts"},
	}
	if !slices.Equal(m.Fix, want) {
		t.Errorf("fix = %+v, want %+v", m.Fix, want)
	}
	if want := m.Title + ` Clear it with: attrib -r "C:\Windows\System32\drivers\etc\hosts" (in an administrator terminal), or run with --no-hosts.`; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Error("no longer ErrReadOnly")
	}
}
