//go:build !windows

package status

import (
	"os"
	"syscall"
)

// fileID is the device and inode a file lives at. An install that renames a
// new build over the old one keeps the path and can keep the size, and the
// inode is what still differs.
type fileID struct{ dev, ino uint64 }

func fileIDOf(info os.FileInfo) fileID {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}
}
