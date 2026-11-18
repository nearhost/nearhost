package hosts

import "path/filepath"

// IsSystemPath reports whether path, made absolute and cleaned, is
// SystemPath(). Links are not followed: the system hosts file and the
// directories above it belong to root or administrators, who alone can
// make them links, while a link elsewhere is refused by its own path.
func IsSystemPath(path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return samePath(abs, filepath.Clean(SystemPath()))
}
