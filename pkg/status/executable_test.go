package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An install replaces the file a running server was started from, and the
// server goes on answering from the build it loaded. What was read at start
// has to be enough to tell, later, that the file at that path is another one:
// replaced by a rename, removed, or rewritten in place.
func TestExecutableSaysWhenItWasReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "enola")
	if err := os.WriteFile(path, []byte("build one"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := ReadExecutable(path)
	if err != nil {
		t.Fatalf("ReadExecutable: %v", err)
	}
	if exe.Path != path || exe.Size != int64(len("build one")) || exe.ModTime.IsZero() {
		t.Fatalf("recorded %+v, want the file's path, size and modification time", exe)
	}
	if exe.Replaced() {
		t.Fatal("an untouched binary reads as replaced")
	}

	// The shape of an install: the new build is written beside the old and
	// renamed over it, so the path names a new file with the same size.
	next := filepath.Join(dir, "enola.new")
	if err := os.WriteFile(next, []byte("build two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if !exe.Replaced() {
		t.Error("a binary renamed over the recorded one does not read as replaced")
	}

	fresh, err := ReadExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	later := fresh.ModTime.Add(2 * time.Second)
	if err := os.WriteFile(path, []byte("build 3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !fresh.Replaced() {
		t.Error("a binary rewritten in place does not read as replaced")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !fresh.Replaced() {
		t.Error("a removed binary does not read as replaced")
	}

	if (Executable{}).Replaced() {
		t.Error("a process that recorded no binary says it was replaced; with nothing recorded there is nothing to compare")
	}
}

func TestSelfExecutableIsThisBinary(t *testing.T) {
	exe, err := SelfExecutable()
	if err != nil {
		t.Fatalf("SelfExecutable: %v", err)
	}
	if !filepath.IsAbs(exe.Path) || exe.Size <= 0 {
		t.Fatalf("SelfExecutable = %+v, want an absolute path and a size", exe)
	}
	if exe.Replaced() {
		t.Error("the running test binary reads as replaced")
	}
}
