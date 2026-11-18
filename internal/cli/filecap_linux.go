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
// security.capability attribute. known is false when the attribute cannot be
// read, as on a filesystem without extended attributes.
func fileNetBindCap(path string) (has, known bool) {
	buf := make([]byte, 64)
	n, err := syscall.Getxattr(path, "security.capability", buf)
	if errors.Is(err, syscall.ENODATA) {
		return false, true
	}
	if err != nil || n < 8 {
		return false, false
	}
	return capDataHasNetBind(buf[:n]), true
}

// capDataHasNetBind reads a security.capability value, the kernel's
// vfs_cap_data: a flags word (bit 0 is "effective"), then the permitted
// and inheritable sets of the first 32 capabilities.
func capDataHasNetBind(data []byte) bool {
	flags := binary.LittleEndian.Uint32(data[0:4])
	permitted := binary.LittleEndian.Uint32(data[4:8])
	return flags&1 != 0 && permitted&(1<<capNetBindService) != 0
}
