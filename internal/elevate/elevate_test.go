package elevate

import "testing"

func TestErrDeclinedSaysItMayHaveGoneUnanswered(t *testing.T) {
	const want = "the Windows prompt was declined or went unanswered"
	if got := ErrDeclined.Error(); got != want {
		t.Fatalf("ErrDeclined = %q, want %q", got, want)
	}
}
