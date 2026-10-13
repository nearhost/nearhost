//go:build linux

package hosts

import (
	"errors"
	"fmt"
	"syscall"
)

// preservedAttrs are the extended attributes carried from the old hosts file
// to its replacement: the SELinux label and the POSIX access ACL.
var preservedAttrs = []string{"security.selinux", "system.posix_acl_access"}

// setXattr is syscall.Setxattr; tests replace it to inject failures.
var setXattr = syscall.Setxattr

// copyLabels carries the SELinux label and POSIX ACL from src to dst.
func copyLabels(src, dst string) error { return copyXattrs(src, dst, preservedAttrs) }

// copyXattrs copies the named attributes from src to dst. An attribute src
// does not have, or a filesystem without xattr support, is skipped. An
// attribute src has but dst cannot take is an error.
func copyXattrs(src, dst string, names []string) error {
	for _, name := range names {
		val, ok, err := getXattr(src, name)
		if err != nil {
			return fmt.Errorf("read %s of %s: %w", name, src, err)
		}
		if !ok {
			continue
		}
		if err := setXattr(dst, name, val, 0); err != nil {
			return fmt.Errorf("copy %s onto %s: %w", name, dst, err)
		}
	}
	return nil
}

// getXattr reads an attribute, reporting ok=false when it is absent or
// unsupported.
func getXattr(path, name string) (val []byte, ok bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		size, err := syscall.Getxattr(path, name, nil)
		if err != nil {
			return nil, false, skippable(err)
		}
		buf := make([]byte, size+1) // spare byte so growth is noticed
		n, err := syscall.Getxattr(path, name, buf)
		if errors.Is(err, syscall.ERANGE) {
			continue // grew between the two calls; ask again
		}
		if err != nil {
			return nil, false, skippable(err)
		}
		if n <= size {
			return buf[:n], true, nil
		}
	}
	return nil, false, fmt.Errorf("attribute %s kept changing size", name)
}

// skippable maps "no such attribute" and "no xattr support" to nil.
func skippable(err error) error {
	if errors.Is(err, syscall.ENODATA) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
		return nil
	}
	return err
}
