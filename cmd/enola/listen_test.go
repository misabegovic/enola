package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --listen takes a value, and every other parser here is an exact-match
// switch, so the pair has to come off the argument list before them without
// disturbing anything around it.
func TestSplitListenFlag(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantRest []string
		wantPath string
		wantErr  bool
	}{
		{name: "absent leaves the arguments untouched", argv: []string{"--no-dashboard", "/repo"}, wantRest: []string{"--no-dashboard", "/repo"}},
		{name: "the path goes with the flag", argv: []string{"--listen", "/run/e.sock", "--no-dashboard", "/repo"}, wantRest: []string{"--no-dashboard", "/repo"}, wantPath: "/run/e.sock"},
		{name: "the joined form", argv: []string{"/repo", "--listen=/run/e.sock"}, wantRest: []string{"/repo"}, wantPath: "/run/e.sock"},
		{name: "a flag with no path is an error", argv: []string{"/repo", "--listen"}, wantErr: true},
		{name: "an empty joined path is an error", argv: []string{"--listen="}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, path, err := splitListenFlag(tt.argv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(rest, tt.wantRest) || path != tt.wantPath {
				t.Errorf("splitListenFlag(%q) = %q, %q; want %q, %q", tt.argv, rest, path, tt.wantRest, tt.wantPath)
			}
		})
	}
}

// socketDir is a short directory for a socket: a unix socket path is limited
// to about a hundred bytes, and a test's temporary directory can be longer.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "enola-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestClaimSocketListensOwnerOnlyAndRemovesTheFileOnRelease(t *testing.T) {
	path := filepath.Join(socketDir(t), "e.sock")
	ln, release, err := claimSocket(path)
	if err != nil {
		t.Fatalf("claimSocket: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("no socket file after the claim: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is %v, not a socket", path, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket permissions %o, want 600: only its owner may connect", perm)
	}
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the claimed socket: %v", err)
	}
	_ = conn.Close()
	release()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the socket file outlived the server: %v", err)
	}
}

// A server killed without cleaning up leaves its socket file behind, and
// nothing answers there. The next server takes the path over rather than
// failing on a file no process is behind.
func TestClaimSocketReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "e.sock")
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	_ = old.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the stale socket file was not left behind: %v", err)
	}

	ln, release, err := claimSocket(path)
	if err != nil {
		t.Fatalf("claimSocket over a stale socket: %v", err)
	}
	defer release()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the replacing server does not answer: %v", err)
	}
	_ = conn.Close()
}

// A path a live server listens on is refused, and the refusal names that
// server's process: two servers must never take turns owning one path.
func TestClaimSocketRefusesALiveServerByPid(t *testing.T) {
	path := filepath.Join(socketDir(t), "e.sock")
	_, release, err := claimSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, again, err := claimSocket(path)
	if err == nil {
		again()
		t.Fatal("a second claim on a live server's socket succeeded")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("the refusal does not name the live server's pid %d: %v", os.Getpid(), err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("the refused claim removed the live server's socket: %v", err)
	}
}

// A socket something else answers on, which never took the claim, is live
// all the same and is refused rather than unlinked from under it.
func TestClaimSocketRefusesAForeignListener(t *testing.T) {
	path := filepath.Join(socketDir(t), "e.sock")
	foreign, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	go func() {
		for {
			c, err := foreign.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	if _, release, err := claimSocket(path); err == nil {
		release()
		t.Fatal("a claim over a socket another process answers on succeeded")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("the refused claim removed the foreign socket: %v", err)
	}
}

// Whatever sits at the path and is not a socket was put there by someone
// else, and is never removed to make room.
func TestClaimSocketRefusesAFileThatIsNotASocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "e.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, release, err := claimSocket(path); err == nil {
		release()
		t.Fatal("a claim over a regular file succeeded")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep me" {
		t.Errorf("the refused claim touched the file: %q, %v", data, err)
	}
}
