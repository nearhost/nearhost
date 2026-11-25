package hosts

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/report"
)

// fakeElevator records the command it is asked to run and answers err.
type fakeElevator struct {
	elevate.Elevator
	got elevate.Command
	err error
}

// Run records c.
func (f *fakeElevator) Run(_ context.Context, c elevate.Command) error {
	f.got = c
	return f.err
}

// The applier asks the elevator to run the hosts helper with the aliases on
// its input, and words a failure as it always has.
func TestElevatedApplierRunsTheHostsHelper(t *testing.T) {
	f := &fakeElevator{}
	if err := ElevatedApplier(context.Background(), f, "/bin/nearhost", "/etc/hosts", "Password: ", true)([]string{"a.test", "b.test"}); err != nil {
		t.Fatal(err)
	}
	want := elevate.Command{
		Path:        "/bin/nearhost",
		Args:        []string{"hosts", "apply", "--file", "/etc/hosts"},
		Input:       "a.test\nb.test\n",
		Prompt:      "Password: ",
		Interactive: true,
	}
	if !reflect.DeepEqual(f.got, want) {
		t.Errorf("command %+v, want %+v", f.got, want)
	}
	f.err = errors.New("boom")
	if err := ElevatedApplier(context.Background(), f, "x", "y", "", false)(nil); err == nil || err.Error() != elevatedApplyPrefix+"boom" {
		t.Errorf("err = %v", err)
	}
}

// When the elevated helper fails on a read-only file, the error is
// hosts.read-only with its steps, as when Nearhost writes the file itself,
// and still matches what the run failed with. A declined prompt is not
// blamed on the file.
func TestElevatedApplierReadOnly(t *testing.T) {
	old := isReadOnly
	isReadOnly = func(string) bool { return true }
	t.Cleanup(func() { isReadOnly = old })
	boom := errors.New("boom")
	f := &fakeElevator{err: boom}
	err := ElevatedApplier(context.Background(), f, "x", `C:\hosts`, "", false)(nil)
	m, ok := report.As(err)
	if !ok || m.Code != report.HostsReadOnly || len(m.Fix) != 2 || !errors.Is(err, ErrReadOnly) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, message %+v", err, m)
	}
	f.err = elevate.ErrDeclined
	err = ElevatedApplier(context.Background(), f, "x", `C:\hosts`, "", false)(nil)
	if errors.Is(err, ErrReadOnly) || !errors.Is(err, elevate.ErrDeclined) {
		t.Errorf("declined: err = %v", err)
	}
}
