//go:build !windows

package cli

// interactiveSession reports true: only Windows tells a core in a sign-in
// session from one in a service's session.
func interactiveSession() (bool, error) { return true, nil }
