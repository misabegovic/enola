package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/enola-labs/enola/internal/filelock"
	"github.com/enola-labs/enola/pkg/status"
)

// listenEnv names the unix socket a server also accepts sessions on, for a
// caller that starts the server through something that passes no arguments.
// --listen wins when both are given.
const listenEnv = "ENOLA_LISTEN"

// claimSocket makes path this process's socket, owner-only, and returns its
// listener and what releases it.
//
// Two servers must never take turns owning one path, so the claim is held by
// a lock beside the socket for the life of the process: the kernel drops it
// when the process dies, however it dies. Under the lock, a socket file that
// nothing answers on is what a killed server left behind, and is replaced; one
// something answers on belongs to a server that never took the lock, and is
// refused like the holder is. A path that is not a socket is someone else's
// file and is never removed. Releasing closes the listener, which removes the
// socket file; the lock file stays, and the next server reuses it.
func claimSocket(path string) (*net.UnixListener, func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("--listen %s: %w", path, err)
	}
	lock, held, err := filelock.TryAcquire(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("--listen %s: taking the lock beside it: %w", abs, err)
	}
	if !held {
		return nil, nil, refusal(abs)
	}
	if info, err := os.Lstat(abs); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			lock.Release()
			return nil, nil, fmt.Errorf("--listen %s: the path exists and is not a socket; refusing to replace it", abs)
		}
		if conn, err := net.DialTimeout("unix", abs, time.Second); err == nil {
			_ = conn.Close()
			lock.Release()
			return nil, nil, refusal(abs)
		}
		if err := os.Remove(abs); err != nil {
			lock.Release()
			return nil, nil, fmt.Errorf("--listen %s: removing the socket a stopped server left: %w", abs, err)
		}
	}
	ln, err := listenOwnerOnly(abs)
	if err != nil {
		lock.Release()
		return nil, nil, fmt.Errorf("--listen %s: %w", abs, err)
	}
	if err := os.Chmod(abs, 0o600); err != nil {
		_ = ln.Close()
		lock.Release()
		return nil, nil, fmt.Errorf("--listen %s: %w", abs, err)
	}
	return ln, func() {
		_ = ln.Close()
		lock.Release()
	}, nil
}

// refusal names the live server behind path: the process at the other end of
// a connection where the platform says, or the instance record naming the
// socket, or neither when both are silent.
func refusal(path string) error {
	pid := 0
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		pid = peerPID(conn)
		_ = conn.Close()
	}
	if pid == 0 {
		for _, inst := range status.LiveInstances() {
			if inst.Socket == path {
				pid = inst.PID
			}
		}
	}
	if pid == 0 {
		return fmt.Errorf("--listen %s: a live server already serves it; refusing to take it over", path)
	}
	return fmt.Errorf("--listen %s: a live server (pid %d) already serves it; refusing to take it over", path, pid)
}
