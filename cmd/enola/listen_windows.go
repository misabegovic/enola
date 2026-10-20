//go:build windows

package main

import "net"

// listenOwnerOnly listens without a mask, which this platform does not have;
// the socket's permissions follow its directory's.
func listenOwnerOnly(path string) (*net.UnixListener, error) {
	return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
}

func peerPID(net.Conn) int { return 0 }
