//go:build windows

package status

import "os"

// fileID is empty here: the size and modification time are what tell one
// build from another on this platform.
type fileID struct{}

func fileIDOf(os.FileInfo) fileID { return fileID{} }
