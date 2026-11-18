//go:build linux

package cli

import (
	"encoding/binary"
	"errors"
	"syscall"
)

// fileNetBindCap reports whether the file at path has cap_net_bind_service
// as an effective, permitted file capability, as `setcap
// cap_net_bind_service=+ep` sets it. The capability is the file's
// security.capability attribute. known is false when it cannot be told.
func fileNetBindCap(path string) (has, known bool) {
	return netBindCapFrom(func(buf []byte) (int, error) {
		return syscall.Getxattr(path, "security.capability", buf)
	})
}

// netBindCapFrom decides from the attribute getxattr reads into buf. An
// attribute that is absent means the file has no capability; any other
// error means it cannot be told.
func netBindCapFrom(getxattr func(buf []byte) (int, error)) (has, known bool) {
	buf := make([]byte, 64)
	n, err := getxattr(buf)
	if errors.Is(err, syscall.ENODATA) {
		return false, true
	}
	if err != nil || n < 0 || n > len(buf) {
		return false, false
	}
	return capDataHasNetBind(buf[:n])
}

// capDataHasNetBind reads a security.capability value, the kernel's
// vfs_cap_data: a flags word (the revision in the top byte, bit 0 for
// "effective"), then the permitted and inheritable sets of the first 32
// capabilities. Revision 1 is 12 bytes, 2 is 20 and 3 is 24, which adds the
// root id of the user namespace the capability applies in. known is false
// for any other length or revision, and for a non-zero root id.
func capDataHasNetBind(data []byte) (has, known bool) {
	if len(data) < 4 {
		return false, false
	}
	flags := binary.LittleEndian.Uint32(data[0:4])
	switch rev := flags >> 24; {
	case rev == 1 && len(data) == 12, rev == 2 && len(data) == 20:
	case rev == 3 && len(data) == 24:
		if binary.LittleEndian.Uint32(data[20:24]) != 0 {
			return false, false
		}
	default:
		return false, false
	}
	permitted := binary.LittleEndian.Uint32(data[4:8])
	return flags&1 != 0 && permitted&(1<<capNetBindService) != 0, true
}
