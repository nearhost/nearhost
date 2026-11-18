//go:build linux

package cli

import (
	"os"
	"testing"
)

func TestCapDataHasNetBind(t *testing.T) {
	// vfs_cap_data v2 as `setcap cap_net_bind_service=+ep` writes it:
	// flags 0x02000001 (revision 2, effective), permitted 0x400.
	v2 := []byte{1, 0, 0, 2, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if !capDataHasNetBind(v2) {
		t.Error("+ep not seen")
	}
	notEffective := append([]byte{0, 0, 0, 2}, v2[4:]...)
	if capDataHasNetBind(notEffective) {
		t.Error("a capability that is not effective was counted")
	}
	other := append([]byte{1, 0, 0, 2, 0, 2}, v2[6:]...)
	if capDataHasNetBind(other) {
		t.Error("another capability was counted")
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
