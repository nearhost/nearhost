//go:build !linux

package cli

// fileNetBindCap says it cannot tell: file capabilities are a Linux
// feature, and nothing calls this elsewhere.
func fileNetBindCap(string) (has, known bool) { return false, false }
