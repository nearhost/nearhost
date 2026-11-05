//go:build !darwin && !linux && !windows

package config

// machineID is "" where Nearhost knows no machine identifier.
func machineID() string { return "" }
