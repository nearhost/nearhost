package proxy

// SetRestrictStorage replaces how the proxy makes its storage folder
// private, and returns what puts the system's way back. It is for tests in
// other packages, which make it fail to see the warning reach the user.
func SetRestrictStorage(f func(root string) error) (restore func()) {
	saved := restrictStorage
	restrictStorage = f
	return func() { restrictStorage = saved }
}
