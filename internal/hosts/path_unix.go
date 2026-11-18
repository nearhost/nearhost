//go:build unix

package hosts

// DefaultPath is the system hosts file, the path Nearhost hands the
// elevated hosts helper.
var DefaultPath = SystemPath()

// SystemPath returns the system hosts file. On macOS /etc is a link to
// /private/etc; the path keeps the /etc spelling Nearhost has always used.
func SystemPath() string { return "/etc/hosts" }

// samePath reports whether two clean absolute paths name the same entry.
// Unix file names keep their case.
func samePath(a, b string) bool { return a == b }
