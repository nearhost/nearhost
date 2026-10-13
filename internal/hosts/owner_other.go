//go:build !unix

package hosts

import (
	"io/fs"
	"os"
)

func chownLike(*os.File, fs.FileInfo) error { return nil }
