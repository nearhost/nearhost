//go:build !windows

package term

import "os"

// enableColor reports whether f can show colour; any terminal can here.
func enableColor(*os.File) bool { return true }
