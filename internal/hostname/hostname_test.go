package hostname

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	ok := []string{"myapp.test", "app.local.redactr.io", "localhost", "*.local.redactr.pro",
		"internal.*.local.redactr.pro", "*.status.local.domaindash.io", "*.myapp.test", "a-b.example.io"}
	for _, h := range ok {
		if err := Valid(h); err != nil {
			t.Errorf("Valid(%q) = %v, want nil", h, err)
		}
	}
	bad := []string{"", "App.example.io", "app.example.io.", "myapp", "*", "*.*", "*.com", "*.co.uk",
		"*.test", "a.*", "127.0.0.1", "-a.example.io", "a_b.example.io", "x*.example.io",
		strings.Repeat("a", 64) + ".example.io"}
	for _, h := range bad {
		if err := Valid(h); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", h)) {
			t.Errorf("Valid(%q) = %v, want an error naming it", h, err)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.local.redactr.pro", "acme.local.redactr.pro", true},
		{"*.local.redactr.pro", "ACME.local.redactr.pro.", true},
		{"*.local.redactr.pro", "local.redactr.pro", false},
		{"*.local.redactr.pro", "a.b.local.redactr.pro", false},
		{"internal.*.local.redactr.pro", "internal.acme.local.redactr.pro", true},
		{"internal.*.local.redactr.pro", "other.acme.local.redactr.pro", false},
		{"auth.local.redactr.pro", "auth.local.redactr.pro", true},
		{"auth.local.redactr.pro", "x.local.redactr.pro", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestLessOrdersExactThenMostLiteral(t *testing.T) {
	in := []string{"*.local.redactr.pro", "internal.*.local.redactr.pro", "auth.local.redactr.pro", "*.*.redactr.pro", "a.test"}
	sort.Slice(in, func(i, j int) bool { return Less(in[i], in[j]) })
	want := []string{"a.test", "auth.local.redactr.pro", "internal.*.local.redactr.pro", "*.local.redactr.pro", "*.*.redactr.pro"}
	if !slices.Equal(in, want) {
		t.Fatalf("order = %v, want %v", in, want)
	}
}

func TestOverlap(t *testing.T) {
	if !Overlap("*.x.example.io", "a.*.example.io") || !Overlap("*.local.redactr.pro", "auth.local.redactr.pro") {
		t.Error("expected overlap")
	}
	if Overlap("*.local.redactr.pro", "internal.*.local.redactr.pro") || Overlap("a.example.io", "b.example.io") {
		t.Error("expected no overlap (different label counts or different exact names)")
	}
}
