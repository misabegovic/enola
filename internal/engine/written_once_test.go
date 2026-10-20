package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/explainers/cycles"
	"github.com/enola-labs/enola/internal/extractors/goextractor"
	"github.com/enola-labs/enola/internal/facts"
)

// Every artifact a generate writes is a new file renamed over its name, the id
// pass writes the facts file it hashes when the caller will write, and the
// copies a cluster, previous/ and a pin used to take are links. These tests pin
// the three properties that make the links safe and the bytes unchanged.

func writtenOnceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	putFile(t, filepath.Join(repo, "go.mod"), "module testmod\n\ngo 1.21\n")
	putFile(t, filepath.Join(repo, "pkg", "a", "a.go"), "package a\n\nimport \"testmod/pkg/b\"\n\nfunc A() { b.B() }\n")
	putFile(t, filepath.Join(repo, "pkg", "b", "b.go"), "package b\n\nfunc B() {}\n")
	return repo
}

func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writtenOnceEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	eng.RegisterExtractor(goextractor.New())
	eng.RegisterExplainer(cycles.New())
	return eng
}

// generateAndWrite walks repos the way the command line does: linking deferred
// on every turn but the last, then the union written to every repository.
func generateAndWrite(t *testing.T, eng *Engine, repos ...string) {
	t.Helper()
	for i, repo := range repos {
		eng.SetDeferLinking(i < len(repos)-1)
		if _, err := eng.GenerateSnapshot(context.Background(), repo, i > 0); err != nil {
			t.Fatalf("generate %s: %v", repo, err)
		}
	}
	eng.SetDeferLinking(false)
	for _, repo := range repos {
		if err := eng.WriteArtifacts(repo); err != nil {
			t.Fatalf("write %s: %v", repo, err)
		}
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	x, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(x, y)
}

// assertNoTempFiles fails on anything a new-file write or a staging step left
// behind in dir: the dot-prefixed temp names every writer here uses.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), "tmp") {
			t.Errorf("%s left behind in %s", e.Name(), dir)
		}
	}
}

// A name linked to an artifact is a read-only share only if nothing ever writes
// through it. A generate that truncated facts.jsonl in place rewrote every other
// name of that file, previous/ and a cluster's siblings included.
func TestWriteArtifacts_ALinkedNameKeepsItsBytes(t *testing.T) {
	repo := writtenOnceRepo(t)
	eng := writtenOnceEngine(t)
	generateAndWrite(t, eng, repo)

	outDir := eng.OutputDir(repo)
	held := t.TempDir()
	before := map[string][]byte{}
	for _, name := range snapshotArtifactFiles {
		src := filepath.Join(outDir, name)
		before[name] = readBytes(t, src)
		if err := os.Link(src, filepath.Join(held, name)); err != nil {
			t.Skipf("the test filesystem refuses links: %v", err)
		}
	}

	putFile(t, filepath.Join(repo, "pkg", "c", "c.go"), "package c\n\nfunc C() {}\n")
	generateAndWrite(t, eng, repo)

	for _, name := range snapshotArtifactFiles {
		if got := readBytes(t, filepath.Join(held, name)); !bytes.Equal(got, before[name]) {
			t.Errorf("%s: a name linked to the first write changed when the second wrote (%d bytes, was %d)", name, len(got), len(before[name]))
		}
	}
	if bytes.Equal(readBytes(t, filepath.Join(outDir, "facts.jsonl")), before["facts.jsonl"]) {
		t.Fatal("the second generate wrote the first one's facts; the test proves nothing")
	}
	assertNoTempFiles(t, outDir)
}

// previous/ and baseline/ hold the bytes of the write they captured, by link
// where the filesystem allows, and keep them across the generates that follow.
func TestRotationAndPin_HoldTheBytesTheyCaptured(t *testing.T) {
	repo := writtenOnceRepo(t)
	eng := writtenOnceEngine(t)
	generateAndWrite(t, eng, repo)
	outDir := eng.OutputDir(repo)

	first := map[string][]byte{}
	for _, name := range snapshotArtifactFiles {
		first[name] = readBytes(t, filepath.Join(outDir, name))
	}
	if err := eng.SetBaseline(repo); err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(outDir, BaselineSubdir)
	if !sameFile(t, filepath.Join(outDir, "facts.jsonl"), filepath.Join(baseDir, "facts.jsonl")) {
		t.Error("the pin copied facts.jsonl where a link was available")
	}

	putFile(t, filepath.Join(repo, "pkg", "c", "c.go"), "package c\n\nfunc C() {}\n")
	generateAndWrite(t, eng, repo)
	prevDir := filepath.Join(outDir, PreviousSubdir)
	if !sameFile(t, filepath.Join(prevDir, "facts.jsonl"), filepath.Join(baseDir, "facts.jsonl")) {
		t.Error("the rotation copied facts.jsonl where a link was available")
	}
	second := readBytes(t, filepath.Join(outDir, "facts.jsonl"))

	putFile(t, filepath.Join(repo, "pkg", "d", "d.go"), "package d\n\nfunc D() {}\n")
	generateAndWrite(t, eng, repo)

	for _, name := range snapshotArtifactFiles {
		if got := readBytes(t, filepath.Join(baseDir, name)); !bytes.Equal(got, first[name]) {
			t.Errorf("baseline/%s no longer holds the pinned write", name)
		}
	}
	if got := readBytes(t, filepath.Join(prevDir, "facts.jsonl")); !bytes.Equal(got, second) {
		t.Error("previous/facts.jsonl does not hold the write before the last one")
	}
	assertNoTempFiles(t, outDir)
	assertNoStagingLeftovers(t, outDir)
}

// failingSerializer writes part of the serialization and then fails, the shape
// of a disk that fills or a process killed mid-write.
func failingSerializer(store *facts.Store, w io.Writer) error {
	_, _ = io.WriteString(w, `{"kind":"half a line`)
	return errors.New("injected serialization failure")
}

func withSerializer(t *testing.T, fn func(*facts.Store, io.Writer) error) {
	t.Helper()
	saved := serializeFacts
	serializeFacts = fn
	t.Cleanup(func() { serializeFacts = saved })
}

// A write that fails midway leaves the facts.jsonl already on disk whole and no
// temp file beside it. In place, the failure left the file truncated.
func TestWriteArtifacts_AFailedWriteLeavesThePreviousFactsWhole(t *testing.T) {
	repo := writtenOnceRepo(t)
	eng := writtenOnceEngine(t)
	generateAndWrite(t, eng, repo)
	outDir := eng.OutputDir(repo)
	before := readBytes(t, filepath.Join(outDir, "facts.jsonl"))

	putFile(t, filepath.Join(repo, "pkg", "c", "c.go"), "package c\n\nfunc C() {}\n")
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatal(err)
	}
	withSerializer(t, failingSerializer)
	if err := eng.WriteArtifacts(repo); err == nil {
		t.Fatal("WriteArtifacts succeeded with a failing serializer")
	}

	if got := readBytes(t, filepath.Join(outDir, "facts.jsonl")); !bytes.Equal(got, before) {
		t.Errorf("facts.jsonl changed under a failed write (%d bytes, was %d)", len(got), len(before))
	}
	for _, name := range []string{"insights.json", "snapshot.meta.json", "receipt.json"} {
		var v any
		if err := json.Unmarshal(readBytes(t, filepath.Join(outDir, name)), &v); err != nil {
			t.Errorf("%s is not whole after a failed write: %v", name, err)
		}
	}
	assertNoTempFiles(t, outDir)
}

// The id pass that writes the file fails the same way: the generate reports the
// error, the facts already on disk stay whole and no staged file is left.
func TestGenerateSnapshot_AFailedStagedWriteLeavesNothing(t *testing.T) {
	repo := writtenOnceRepo(t)
	eng := writtenOnceEngine(t)
	generateAndWrite(t, eng, repo)
	outDir := eng.OutputDir(repo)
	before := readBytes(t, filepath.Join(outDir, "facts.jsonl"))

	eng.SetWritesArtifacts(true)
	withSerializer(t, failingSerializer)
	putFile(t, filepath.Join(repo, "pkg", "c", "c.go"), "package c\n\nfunc C() {}\n")
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err == nil {
		t.Fatal("GenerateSnapshot succeeded with a failing serializer")
	}
	if got := readBytes(t, filepath.Join(outDir, "facts.jsonl")); !bytes.Equal(got, before) {
		t.Error("facts.jsonl changed under a failed id pass")
	}
	assertNoTempFiles(t, outDir)
}

// countingSerializer counts serializations and otherwise serializes as the
// store does.
func countingSerializer(n *int) func(*facts.Store, io.Writer) error {
	return func(store *facts.Store, w io.Writer) error {
		*n++
		return store.WriteJSONL(w)
	}
}

// A caller that says it will write gets one serialization per generate and
// write, not two, and the same bytes, output hashes and snapshot id as the
// caller that does not say so. A caller that does not say so leaves no file.
func TestWritesArtifacts_OneSerializationSameBytes(t *testing.T) {
	type result struct {
		facts    []byte
		receipt  facts.Receipt
		serialns int
	}
	// One tree for both runs: the facts name the repository by its directory.
	repo := writtenOnceRepo(t)
	run := func(writes bool) result {
		eng := writtenOnceEngine(t)
		outDir := eng.OutputDir(repo)
		if err := os.RemoveAll(outDir); err != nil {
			t.Fatal(err)
		}
		n := 0
		withSerializer(t, countingSerializer(&n))
		eng.SetWritesArtifacts(writes)
		if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
			t.Fatal(err)
		}
		if !writes {
			if _, err := os.Stat(filepath.Join(outDir, "facts.jsonl")); !os.IsNotExist(err) {
				t.Errorf("a generate whose caller does not write left facts in %s (err=%v)", outDir, err)
			}
			assertNoTempFiles(t, outDir)
		}
		if err := eng.WriteArtifacts(repo); err != nil {
			t.Fatal(err)
		}
		var r facts.Receipt
		if err := json.Unmarshal(readBytes(t, filepath.Join(outDir, "receipt.json")), &r); err != nil {
			t.Fatal(err)
		}
		assertNoTempFiles(t, outDir)
		return result{facts: readBytes(t, filepath.Join(outDir, "facts.jsonl")), receipt: r, serialns: n}
	}

	hashOnly, written := run(false), run(true)
	if hashOnly.serialns != 2 || written.serialns != 1 {
		t.Errorf("serializations: %d without the declaration, %d with it; want 2 and 1", hashOnly.serialns, written.serialns)
	}
	if !bytes.Equal(hashOnly.facts, written.facts) {
		t.Error("facts.jsonl differs between the two paths")
	}
	if hashOnly.receipt.SnapshotID == "" || hashOnly.receipt.SnapshotID != written.receipt.SnapshotID {
		t.Errorf("snapshot id differs: %q vs %q", hashOnly.receipt.SnapshotID, written.receipt.SnapshotID)
	}
	for _, name := range []string{"facts.jsonl", "insights.json"} {
		if hashOnly.receipt.OutputHashes[name] == "" || hashOnly.receipt.OutputHashes[name] != written.receipt.OutputHashes[name] {
			t.Errorf("output hash of %s differs: %q vs %q", name, hashOnly.receipt.OutputHashes[name], written.receipt.OutputHashes[name])
		}
	}
	if got, want := written.receipt.OutputHashes["facts.jsonl"], sha256Prefixed(written.facts); got != want {
		t.Errorf("receipt names %s for facts.jsonl, the file hashes to %s", got, want)
	}
}

// A file staged for a write that never comes is removed by the next generate,
// which replaces the store it was serialized from; the write that follows takes
// the new store's facts, never the stale file.
func TestWritesArtifacts_AStagedFileNobodyWroteIsRemoved(t *testing.T) {
	repo := writtenOnceRepo(t)
	eng := writtenOnceEngine(t)
	eng.SetWritesArtifacts(true)
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(repo, "pkg", "c", "c.go"), "package c\n\nfunc C() {}\n")
	eng.SetWritesArtifacts(false)
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatal(err)
	}
	outDir := eng.OutputDir(repo)
	assertNoTempFiles(t, outDir)

	if err := eng.WriteArtifacts(repo); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readBytes(t, filepath.Join(outDir, "facts.jsonl")), []byte("pkg/c")) {
		t.Error("the write took the facts of the generate before the last")
	}
	assertNoTempFiles(t, outDir)
}

// A cluster writes one union: the first repository's facts.jsonl is the one
// file written, and every later repository holds a link to it.
func TestCluster_LaterRepositoriesLinkTheFirstFacts(t *testing.T) {
	repos := []string{writtenOnceRepo(t), writtenOnceRepo(t), writtenOnceRepo(t)}
	eng := writtenOnceEngine(t)
	eng.SetWritesArtifacts(true)
	generateAndWrite(t, eng, repos...)

	first := filepath.Join(eng.OutputDir(repos[0]), "facts.jsonl")
	for _, repo := range repos[1:] {
		if !sameFile(t, first, filepath.Join(eng.OutputDir(repo), "facts.jsonl")) {
			t.Errorf("%s copied the union's facts.jsonl where a link was available", repo)
		}
	}
	for _, repo := range repos {
		assertNoTempFiles(t, eng.OutputDir(repo))
	}
}

// withLinkFailure makes every link fail the way a link across filesystems does.
func withLinkFailure(t *testing.T) {
	t.Helper()
	saved := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkFile = saved })
}

// A link the filesystem refuses falls back to a copy of the same bytes, for a
// cluster's later repositories, the rotation and the pin alike.
func TestLinkFailure_FallsBackToACopyOfTheSameBytes(t *testing.T) {
	withLinkFailure(t)
	repos := []string{writtenOnceRepo(t), writtenOnceRepo(t)}
	eng := writtenOnceEngine(t)
	eng.SetWritesArtifacts(true)
	generateAndWrite(t, eng, repos...)

	first := filepath.Join(eng.OutputDir(repos[0]), "facts.jsonl")
	second := filepath.Join(eng.OutputDir(repos[1]), "facts.jsonl")
	if sameFile(t, first, second) {
		t.Fatal("a refused link still produced a link")
	}
	if !bytes.Equal(readBytes(t, first), readBytes(t, second)) {
		t.Error("the copy fallback wrote different bytes")
	}

	outDir := eng.OutputDir(repos[0])
	if err := eng.SetBaseline(repos[0]); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(outDir, BaselineSubdir, "facts.jsonl")
	if sameFile(t, first, pinned) || !bytes.Equal(readBytes(t, first), readBytes(t, pinned)) {
		t.Error("the pin did not fall back to a copy of the same bytes")
	}

	before := readBytes(t, first)
	generateAndWrite(t, eng, repos...)
	previous := filepath.Join(outDir, PreviousSubdir, "facts.jsonl")
	if !bytes.Equal(readBytes(t, previous), before) {
		t.Error("the rotation did not fall back to a copy of the same bytes")
	}
	for _, repo := range repos {
		assertNoTempFiles(t, eng.OutputDir(repo))
		assertNoStagingLeftovers(t, eng.OutputDir(repo))
	}
}
