package sudo

import "io/fs"

// OwnedByRoot is false on Windows, which has no root and no sudo, so Binary
// finds none and Needed is false. Tests replace it.
var OwnedByRoot = func(fs.FileInfo) bool { return false }

// runnable is false on Windows: there is no sudo to run.
func runnable(string) bool { return false }
