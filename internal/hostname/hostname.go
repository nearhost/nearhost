// Package hostname validates, matches and orders the host names a project
// serves, including patterns with wildcard labels such as
// internal.*.local.example.io.
package hostname

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// wildcard is the label that stands for exactly one non-empty label.
const wildcard = "*"

// Valid reports whether h is a usable exact host or pattern. The error names h
// so callers can pass it straight to the user. Names must already be lower
// case and without a trailing dot, so one project cannot spell the same host
// two ways. A bare label is only allowed for localhost.
func Valid(h string) error {
	fail := func(why string) error { return fmt.Errorf("invalid host %q: %s", h, why) }
	if h == "" || len(h) > 253 {
		return fail("must be 1 to 253 characters")
	}
	if h != strings.ToLower(h) {
		return fail("must be lower case")
	}
	if strings.HasSuffix(h, ".") {
		return fail("must not end with a dot")
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return fail("must be a name, not an IP address")
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == wildcard {
			continue
		}
		if !validLabel(l) {
			return fail("labels are letters, digits and hyphens, up to 63 characters, with no leading or trailing hyphen; * must be a whole label")
		}
	}
	last := -1
	for i, l := range labels {
		if l == wildcard {
			last = i
		}
	}
	if last < 0 {
		if len(labels) < 2 && h != "localhost" {
			return fail("needs at least two labels")
		}
		return nil
	}
	// A wildcard must sit under a name the user could own. Everything right of
	// the right-most * has to be more than a public suffix, otherwise one
	// pattern would claim a whole TLD such as *.com or *.co.uk. Unknown TLDs
	// count as suffixes, so *.test is rejected while *.myapp.test is allowed.
	tail := strings.Join(labels[last+1:], ".")
	if len(labels)-last-1 < 2 {
		return fail("a wildcard needs a name of at least two labels to the right of it")
	}
	if suffix, _ := publicsuffix.PublicSuffix(tail); suffix == tail {
		return fail("a wildcard cannot sit directly under a public suffix")
	}
	return nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// IsPattern reports whether h has a * label.
func IsPattern(h string) bool {
	return slices.Contains(strings.Split(h, "."), wildcard)
}

// Match reports whether name is served by pattern. The name is compared case
// insensitively and a trailing dot is ignored, because clients send both. A *
// matches exactly one non-empty label, and an exact pattern matches only
// itself.
func Match(pattern, name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	p := strings.Split(pattern, ".")
	n := strings.Split(name, ".")
	if len(p) != len(n) {
		return false
	}
	for i := range p {
		if n[i] == "" {
			return false
		}
		if p[i] != wildcard && p[i] != n[i] {
			return false
		}
	}
	return true
}

// literals counts the labels of h that are not wildcards.
func literals(h string) int {
	n := 0
	for _, l := range strings.Split(h, ".") {
		if l != wildcard {
			n++
		}
	}
	return n
}

// Less orders hosts from most to least specific, so the first match in a sorted
// list is the right route: exact hosts (lexically, since they cannot overlap) before
// patterns, then patterns with more literal labels, then lexically to keep the
// order stable.
func Less(a, b string) bool {
	pa, pb := IsPattern(a), IsPattern(b)
	if pa != pb {
		return !pa
	}
	if !pa {
		return a < b
	}
	if la, lb := literals(a), literals(b); la != lb {
		return la > lb
	}
	return a < b
}

// Overlap reports whether some name matches both a and b. Names only match
// patterns with the same number of labels, and each label pair must be equal or
// have a * on either side.
func Overlap(a, b string) bool {
	la, lb := strings.Split(a, "."), strings.Split(b, ".")
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] && la[i] != wildcard && lb[i] != wildcard {
			return false
		}
	}
	return true
}
