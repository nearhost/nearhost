//go:build !windows

package hosts

// elevatedApplyPrefix starts the error of a failed elevated apply.
const elevatedApplyPrefix = "sudo hosts apply: "
