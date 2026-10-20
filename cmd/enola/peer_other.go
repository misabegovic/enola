//go:build !linux && !windows

package main

import "net"

// peerPID is 0 where the kernel is not asked; the refusal then takes the pid
// from the instance record that names the socket.
func peerPID(net.Conn) int { return 0 }
