//go:build unix

package hosts

// DefaultPath is the system hosts file, the path Nearhost hands the
// elevated hosts helper.
var DefaultPath = SystemPath()

// SystemPath returns the system hosts file. On macOS /etc is a link to
// /private/etc; the path keeps the /etc spelling Nearhost has always used.
func SystemPath() string { return "/etc/hosts" }

// IsSystemPath reports whether path is SystemPath() exactly, as Nearhost
// spells it. Nothing is cleaned: the kernel resolves a ".." element after
// it follows the link before it, so a path that cleans to /etc/hosts can
// open another file, and a relative path depends on the working directory.
// /etc/hosts itself, and /etc, belong to root, who alone can make them links.
func IsSystemPath(path string) bool { return path == SystemPath() }
