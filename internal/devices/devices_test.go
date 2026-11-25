package devices

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func newStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s := Open(filepath.Join(t.TempDir(), "sub", "devices.json"), testKey)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestPairCodeSingleUseAndExpiry(t *testing.T) {
	s, now := newStore(t)
	pc := s.NewPairCode()
	if len(pc.Code) != 8 || strings.Trim(pc.Code, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789") != "" {
		t.Fatalf("bad code %q", pc.Code)
	}
	if !pc.Expires.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("expires %v", pc.Expires)
	}
	if dev, paired, expired := s.PairStatus(pc.Code); dev != "" || paired || expired {
		t.Fatalf("fresh status %q %v %v", dev, paired, expired)
	}
	tok, err := s.Pair(pc.Code, "iPad", "1.2.3.4")
	if err != nil || tok == "" {
		t.Fatalf("pair: %v", err)
	}
	if dev, paired, _ := s.PairStatus(pc.Code); dev != "iPad" || !paired {
		t.Fatalf("status after pair %q %v", dev, paired)
	}
	if _, err := s.Pair(pc.Code, "iPad", "1.2.3.4"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second pair: %v", err)
	}
	pc2 := s.NewPairCode()
	*now = now.Add(10*time.Minute + time.Second)
	if _, err := s.Pair(pc2.Code, "x", "1.2.3.4"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired pair: %v", err)
	}
	if _, paired, expired := s.PairStatus(pc2.Code); paired || !expired {
		t.Fatalf("expired status %v %v", paired, expired)
	}
	if _, err := s.Pair("NOSUCHCD", "x", "1.2.3.4"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("unknown pair: %v", err)
	}
}

func TestPairedDeviceSurvivesReopen(t *testing.T) {
	s, _ := newStore(t)
	tok, err := s.Pair(s.NewPairCode().Code, "iPad", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	s2 := Open(s.path, testKey)
	d, ok := s2.Device(tok)
	if !ok || d.Name != "iPad" || d.LastAddr != "1.2.3.4" || len(d.ID) != 32 {
		t.Fatalf("device %+v %v", d, ok)
	}
	if _, ok := s2.Device(tok + "x"); ok {
		t.Fatal("wrong token accepted")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(tok)) {
		t.Fatal("file holds the token")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(s.path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", info.Mode())
		}
	}
}

func TestDuplicateNamesAreNumbered(t *testing.T) {
	s, _ := newStore(t)
	for _, want := range []string{"iPad", "iPad 2", "iPad 3"} {
		if _, err := s.Pair(s.NewPairCode().Code, "iPad", "a"); err != nil {
			t.Fatal(err)
		}
		_ = want
	}
	list, err := s.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %v", list, err)
	}
	for i, want := range []string{"iPad", "iPad 2", "iPad 3"} {
		if list[i].Name != want {
			t.Fatalf("names %v", list)
		}
	}
}

func TestAuthTokenSingleUseBoundToHostAndExpires(t *testing.T) {
	s, now := newStore(t)
	tok, _ := s.Pair(s.NewPairCode().Code, "iPad", "a")
	d, _ := s.Device(tok)
	a := s.NewAuthToken(d.ID, "a.x")
	if _, ok := s.RedeemAuth(a, "b.x"); ok {
		t.Fatal("wrong host accepted")
	}
	if id, ok := s.RedeemAuth(a, "A.x"); !ok || id != d.ID {
		t.Fatalf("first use %q %v", id, ok)
	}
	if _, ok := s.RedeemAuth(a, "a.x"); ok {
		t.Fatal("second use accepted")
	}
	b := s.NewAuthToken(d.ID, "a.x")
	*now = now.Add(61 * time.Second)
	if _, ok := s.RedeemAuth(b, "a.x"); ok {
		t.Fatal("expired accepted")
	}
}

func TestSiteCookieBoundToHost(t *testing.T) {
	s, _ := newStore(t)
	tok, _ := s.Pair(s.NewPairCode().Code, "iPad", "a")
	d, _ := s.Device(tok)
	c := s.SiteCookie(d.ID, "a.x")
	if id, ok := s.CheckSite(c, "a.x"); !ok || id != d.ID {
		t.Fatalf("good cookie %q %v", id, ok)
	}
	if _, ok := s.CheckSite(c, "b.x"); ok {
		t.Fatal("other host accepted")
	}
	if _, ok := s.CheckSite(c[:len(c)-2]+"AA", "a.x"); ok {
		t.Fatal("tampered accepted")
	}
	if _, ok := s.CheckSite("nodot", "a.x"); ok {
		t.Fatal("malformed accepted")
	}
}

func TestSiteCookieFailsAfterRevoke(t *testing.T) {
	s, _ := newStore(t)
	tok, _ := s.Pair(s.NewPairCode().Code, "iPad", "a")
	d, _ := s.Device(tok)
	c := s.SiteCookie(d.ID, "a.x")
	if _, ok := s.CheckSite(c, "a.x"); !ok {
		t.Fatal("not ok before revoke")
	}
	if err := s.Revoke("iPad"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.CheckSite(c, "a.x"); ok {
		t.Fatal("ok after revoke")
	}
	if _, ok := s.Device(tok); ok {
		t.Fatal("device ok after revoke")
	}
	if err := s.Revoke("iPad"); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("revoke again: %v", err)
	}

	// A revoke by another Store on the same file is seen.
	tok2, _ := s.Pair(s.NewPairCode().Code, "Phone", "a")
	d2, _ := s.Device(tok2)
	c2 := s.SiteCookie(d2.ID, "a.x")
	if _, ok := s.CheckSite(c2, "a.x"); !ok {
		t.Fatal("phone not ok")
	}
	other := Open(s.path, testKey)
	if err := other.Revoke("Phone"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.CheckSite(c2, "a.x"); ok {
		t.Fatal("cross-store revoke not seen by CheckSite")
	}
	if _, ok := s.Device(tok2); ok {
		t.Fatal("cross-store revoke not seen by Device")
	}
}

func TestLastSeenWritesAtMostOncePerMinute(t *testing.T) {
	s, now := newStore(t)
	tok, _ := s.Pair(s.NewPairCode().Code, "iPad", "a")
	*now = now.Add(2 * time.Minute)
	s.Device(tok)
	st1, _ := os.Stat(s.path)
	b1, _ := os.ReadFile(s.path)
	*now = now.Add(30 * time.Second)
	s.Device(tok)
	b2, _ := os.ReadFile(s.path)
	if !bytes.Equal(b1, b2) {
		t.Fatal("file rewritten within a minute")
	}
	*now = now.Add(31 * time.Second)
	s.Device(tok)
	b3, _ := os.ReadFile(s.path)
	if bytes.Equal(b1, b3) {
		t.Fatal("file not rewritten after a minute")
	}
	_ = st1
}

func TestAllowLimitsFailures(t *testing.T) {
	s, now := newStore(t)
	for i := 0; i < 10; i++ {
		if !s.Allow("1.1.1.1") {
			t.Fatalf("blocked after %d", i)
		}
		s.Pair("BADCODE2", "x", "1.1.1.1")
	}
	if s.Allow("1.1.1.1") {
		t.Fatal("not blocked after 10 failures")
	}
	if !s.Allow("2.2.2.2") {
		t.Fatal("other addr blocked")
	}
	*now = now.Add(10*time.Minute + time.Second)
	if !s.Allow("1.1.1.1") {
		t.Fatal("still blocked after 10 minutes")
	}
	for i := 0; i < 10; i++ {
		s.Fail("3.3.3.3")
	}
	if s.Allow("3.3.3.3") {
		t.Fatal("Fail not counted")
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _ := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := s.Pair(s.NewPairCode().Code, "iPad", "a")
			if err != nil {
				t.Error(err)
				return
			}
			d, _ := s.Device(tok)
			s.CheckSite(s.SiteCookie(d.ID, "a.x"), "a.x")
			s.Allow("a")
			s.List()
		}()
	}
	wg.Wait()
	if l, _ := s.List(); len(l) != 8 {
		t.Fatalf("%d devices", len(l))
	}
}
