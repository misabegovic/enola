package status

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A caller that wants to share a running server finds it the way --status
// does, through the instance registry, and needs nothing else: the record
// names the socket, and connecting to what it names reaches the server.
func TestInstanceRecordNamesTheSocketACallerConnectsTo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sock := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("ok"))
		_ = c.Close()
	}()

	tr := NewTracker("/tmp/example-repo")
	tr.SetIdentity(Identity{Binary: "enola", Version: "1.2.3", Socket: sock})
	tr.PersistStartup()
	defer tr.Close()

	var found string
	for _, inst := range LiveInstances() {
		if inst.PID == os.Getpid() {
			found = inst.Socket
		}
	}
	if found != sock {
		t.Fatalf("the instance record names socket %q, want %q", found, sock)
	}
	conn, err := net.Dial("unix", found)
	if err != nil {
		t.Fatalf("connecting to the socket the record names: %v", err)
	}
	defer conn.Close()
	got, _ := io.ReadAll(conn)
	if string(got) != "ok" {
		t.Errorf("read %q through the recorded socket, want the listener's answer", got)
	}
}

// The record says which file the process was started from and whether that
// file is still the one it runs, so an operator listing servers sees the one
// answering from a build that is no longer installed.
func TestInstanceRecordNamesTheBinaryAndWhenItWasReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := filepath.Join(t.TempDir(), "enola")
	if err := os.WriteFile(bin, []byte("build one"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := ReadExecutable(bin)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTracker("/tmp/example-repo")
	tr.SetIdentity(Identity{Binary: "enola", Version: "1.2.3", Executable: exe})
	tr.PersistStartup()
	defer tr.Close()

	rec := ownRecord(t)
	if rec.BinaryPath != bin || rec.BinarySize != exe.Size || !rec.BinaryModTime.Equal(exe.ModTime) {
		t.Errorf("record names binary %q (%d bytes, %v), want %q (%d bytes, %v)",
			rec.BinaryPath, rec.BinarySize, rec.BinaryModTime, bin, exe.Size, exe.ModTime)
	}
	if rec.BinaryReplaced {
		t.Error("the record says an untouched binary was replaced")
	}

	next := bin + ".new"
	if err := os.WriteFile(next, []byte("build two"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A reader has the path, size and time and not the inode, so the new
	// build is given a later time, as a real install's is.
	later := exe.ModTime.Add(2 * time.Second)
	if err := os.Chtimes(next, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
	if rec := ownRecord(t); !rec.RunsReplacedBinary() {
		t.Error("a reader of the record written before the replacement cannot tell the binary was replaced")
	}
	tr.persistInstance()
	if rec := ownRecord(t); !rec.BinaryReplaced {
		t.Error("after the binary was replaced the record still says it was not")
	}
}

// A server that listens on no socket and was told nothing about its binary
// writes the record it always wrote: none of the new keys appear.
func TestInstanceRecordWithoutSocketOrBinaryAddsNoKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tr := NewTracker("/tmp/example-repo")
	tr.SetIdentity(Identity{Binary: "enola", Version: "1.2.3"})
	tr.PersistStartup()
	defer tr.Close()

	dir, err := instancesDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one record, got %d (%v)", len(entries), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"socket", "binary_path", "binary_size", "binary_mod_time", "binary_replaced"} {
		if _, ok := keys[k]; ok {
			t.Errorf("record carries %q with nothing to say:\n%s", k, data)
		}
	}
}

func ownRecord(t *testing.T) Instance {
	t.Helper()
	for _, inst := range LiveInstances() {
		if inst.PID == os.Getpid() {
			return inst
		}
	}
	t.Fatal("no instance record for this process")
	return Instance{}
}
