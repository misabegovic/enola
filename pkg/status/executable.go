package status

import (
	"os"
	"path/filepath"
	"time"
)

// Executable is a binary file as it was when read. A server is long-lived and
// an install replaces the file it was started from while it runs; the process
// goes on answering from the build it loaded, and nothing else says so. What
// is kept here is enough to tell, later, that the file at the same path is
// another one.
type Executable struct {
	Path    string
	Size    int64
	ModTime time.Time
	id      fileID
}

// ReadExecutable records the file at path as it is now.
func ReadExecutable(path string) (Executable, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Executable{}, err
	}
	return Executable{Path: path, Size: info.Size(), ModTime: info.ModTime(), id: fileIDOf(info)}, nil
}

// SelfExecutable records the binary this process runs, by the path its links
// resolve to, since an install replaces that file and not the link to it.
func SelfExecutable() (Executable, error) {
	path, err := os.Executable()
	if err != nil {
		return Executable{}, err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return ReadExecutable(path)
}

// Replaced reports whether the file at the recorded path is no longer the one
// recorded: gone, another file renamed over it, or the same file rewritten.
// An empty record is never replaced, since there is nothing to compare.
func (e Executable) Replaced() bool {
	if e.Path == "" {
		return false
	}
	info, err := os.Stat(e.Path)
	if err != nil {
		return true
	}
	return info.Size() != e.Size || !info.ModTime().Equal(e.ModTime) || fileIDOf(info) != e.id
}
