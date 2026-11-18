package config

// syncDir does nothing on Windows. Go cannot flush a directory handle there
// (FlushFileBuffers on one fails), and NTFS journals the rename itself, so
// there is no step to take and no error worth reporting.
func syncDir(string) error { return nil }
