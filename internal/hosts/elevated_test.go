package hosts

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nearhost/nearhost/internal/elevate"
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
	if err := ElevatedApplier(context.Background(), f, "x", "y", "", false)(nil); err == nil || err.Error() != "sudo hosts apply: boom" {
		t.Errorf("err = %v", err)
	}
}
