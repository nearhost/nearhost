package proxy

import "testing"

func TestNSSIsNotNeededOnWindows(t *testing.T) {
	if st := CheckNSS(nil); st.State != NSSNotNeeded {
		t.Errorf("CheckNSS = %+v, want NSSNotNeeded", st)
	}
}
