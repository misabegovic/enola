//go:build !windows

package main

import (
	"net"
	"syscall"
)

// listenOwnerOnly creates the socket with no permission for anyone but its
// owner from the moment it exists, rather than tightening it after: between
// the two another user could connect. The mask is process-wide, and this runs
// before the server starts anything that creates files.
func listenOwnerOnly(path string) (*net.UnixListener, error) {
	old := syscall.Umask(0o177)
	defer syscall.Umask(old)
	return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
}
