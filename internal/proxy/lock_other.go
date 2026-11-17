//go:build !windows

package proxy

// lockSettling reports whether a failed lock acquisition may have met a lock
// file in the middle of its removal. Unix removes a file at once, so a
// failure there is certmagic's to report.
func lockSettling(string) bool { return false }
