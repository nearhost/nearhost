package nearhostrc

import (
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// The errors doctor tells apart carry the codes doctor reports them with.
func TestErrorsCarryDoctorsCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code report.Code
		step string
	}{
		{"version", &VersionError{Version: 2}, report.NearhostrcVersion, "Update Nearhost."},
		{"unset", &UnsetError{Var: "API_HOST", Line: 4}, report.NearhostrcUnsetVariable,
			"Set API_HOST in .env.local or .env, or give it a default in .nearhostrc: ${API_HOST:-value}."},
		{"bare", &BareVarError{Err: errors.New(".nearhostrc: bad host"), Var: "HOST", Line: 3}, report.NearhostrcInvalid, "Correct .nearhostrc."},
	} {
		m, ok := report.As(tc.err)
		if !ok {
			t.Fatalf("%s: no message", tc.name)
		}
		if m.Code != tc.code || m.Title != tc.err.Error() {
			t.Errorf("%s: %q %q, want %q titled %q", tc.name, m.Code, m.Title, tc.code, tc.err.Error())
		}
		if len(m.Fix) == 0 || m.Fix[0].Text != tc.step {
			t.Errorf("%s: fix = %+v, want %q", tc.name, m.Fix, tc.step)
		}
	}
	bare := &BareVarError{Err: errors.New(".nearhostrc: bad host"), Var: "HOST"}
	if !errors.Is(bare, bare.Err) {
		t.Error("BareVarError no longer wraps its error")
	}
}
