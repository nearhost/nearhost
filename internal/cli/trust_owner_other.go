//go:build !unix

package cli

// ownerProblem is the Unix check for root deleting another user's CA. Other
// systems have no such check.
func ownerProblem(string) error { return nil }
