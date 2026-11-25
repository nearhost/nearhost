package hosts

import (
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// A read-only hosts file is hosts.read-only, with how to clear it as the
// step, while the error's text stays as it was.
func TestReadOnlyIsAMessage(t *testing.T) {
	err := error(&readOnlyError{path: `C:\Windows\System32\drivers\etc\hosts`})
	m, ok := report.As(err)
	if !ok || m.Code != report.HostsReadOnly {
		t.Fatalf("message = %+v, want %q", m, report.HostsReadOnly)
	}
	if m.Title != `C:\Windows\System32\drivers\etc\hosts is read-only.` {
		t.Errorf("title = %q", m.Title)
	}
	step := `Clear it with: attrib -r "C:\Windows\System32\drivers\etc\hosts" (in an administrator terminal), or run with --no-hosts.`
	if len(m.Fix) != 1 || m.Fix[0].Text != step {
		t.Errorf("fix = %+v, want %q", m.Fix, step)
	}
	if err.Error() != m.Title+" "+step {
		t.Errorf("Error() = %q", err.Error())
	}
	if !errors.Is(err, ErrReadOnly) {
		t.Error("no longer ErrReadOnly")
	}
}
