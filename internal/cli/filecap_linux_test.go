//go:build linux

package cli

import (
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"testing"
)

// capData builds a security.capability value of the revision's length.
func capData(rev, flags, permitted, rootid uint32) []byte {
	size := map[uint32]int{1: 12, 2: 20, 3: 24}[rev]
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b[0:], rev<<24|flags)
	binary.LittleEndian.PutUint32(b[4:], permitted)
	if rev == 3 {
		binary.LittleEndian.PutUint32(b[20:], rootid)
	}
	return b
}

func TestCapDataHasNetBind(t *testing.T) {
	const bit = 1 << 10
	for _, tc := range []struct {
		name       string
		data       []byte
		has, known bool
	}{
		{"revision 1 +ep", capData(1, 1, bit, 0), true, true},
		{"revision 2 +ep", capData(2, 1, bit, 0), true, true},
		{"revision 3 +ep", capData(3, 1, bit, 0), true, true},
		{"revision 1 without the bit", capData(1, 1, 1<<9, 0), false, true},
		{"revision 2 without the bit", capData(2, 1, 0, 0), false, true},
		{"revision 3 without the bit", capData(3, 1, 1<<9, 0), false, true},
		{"revision 1 not effective", capData(1, 0, bit, 0), false, true},
		{"revision 2 not effective", capData(2, 0, bit, 0), false, true},
		{"revision 3 not effective", capData(3, 0, bit, 0), false, true},
		{"revision 3 with a root id", capData(3, 1, bit, 1000), false, false},
		{"short", []byte{1, 0}, false, false},
		{"empty", nil, false, false},
		{"garbage", []byte("not a capability at all"), false, false},
		{"unknown revision", append([]byte{1, 0, 0, 9}, make([]byte, 16)...), false, false},
		{"revision 2 with revision 1's length", func() []byte { d := capData(1, 1, bit, 0); d[3] = 2; return d }(), false, false},
	} {
		if has, known := capDataHasNetBind(tc.data); has != tc.has || known != tc.known {
			t.Errorf("%s: has %v known %v, want %v %v", tc.name, has, known, tc.has, tc.known)
		}
	}
}

func TestNetBindCapFromMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		has, known bool
	}{
		{"no attribute", syscall.ENODATA, false, true},
		{"not supported", syscall.ENOTSUP, false, false},
		{"permission", syscall.EACCES, false, false},
		{"other", errors.New("boom"), false, false},
	} {
		has, known := netBindCapFrom(func([]byte) (int, error) { return -1, tc.err })
		if has != tc.has || known != tc.known {
			t.Errorf("%s: has %v known %v, want %v %v", tc.name, has, known, tc.has, tc.known)
		}
	}
	has, known := netBindCapFrom(func(b []byte) (int, error) { return copy(b, capData(2, 1, 1<<10, 0)), nil })
	if !has || !known {
		t.Errorf("a +ep value: has %v known %v", has, known)
	}
}

func TestFileNetBindCapOnAFileWithoutOne(t *testing.T) {
	f := t.TempDir() + "/x"
	if err := os.WriteFile(f, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if has, _ := fileNetBindCap(f); has {
		t.Error("a fresh file has no capability")
	}
}
