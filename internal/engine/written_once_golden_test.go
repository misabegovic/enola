package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
)

// writtenArtifacts is what one member's output dir holds after a write, less
// the fields that carry a clock.
type writtenArtifacts struct {
	facts      []byte
	insights   []byte
	llmContext []byte
	snapshotID string
	hashes     map[string]string
}

// walkAndWrite drives a fixture the way the command line does, linking deferred
// on every turn but the last and the union written to every member, with or
// without declaring the write to the engine beforehand.
func walkAndWrite(t *testing.T, f fixture, root, configPath string, declared bool) []writtenArtifacts {
	t.Helper()
	eng, _, err := bootstrap.NewEngine(bootstrap.Options{ConfigPath: configPath})
	if err != nil {
		t.Fatalf("bootstrap.NewEngine: %v", err)
	}
	eng.SetWritesArtifacts(declared)
	repos := make([]string, len(f.subRepos))
	for i, sub := range f.subRepos {
		repos[i] = root
		if sub != "." {
			repos[i] = filepath.Join(root, sub)
		}
	}
	for i, repo := range repos {
		eng.SetDeferLinking(i < len(repos)-1)
		if _, err := eng.GenerateSnapshot(context.Background(), repo, i > 0); err != nil {
			t.Fatalf("generate %s: %v", repo, err)
		}
	}
	out := make([]writtenArtifacts, len(repos))
	for _, repo := range repos {
		if err := eng.WriteArtifacts(repo); err != nil {
			t.Fatalf("write %s: %v", repo, err)
		}
	}
	for i, repo := range repos {
		dir := eng.OutputDir(repo)
		read := func(name string) []byte {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return b
		}
		var r facts.Receipt
		if err := json.Unmarshal(read("receipt.json"), &r); err != nil {
			t.Fatalf("%s receipt: %v", repo, err)
		}
		out[i] = writtenArtifacts{
			facts:      read("facts.jsonl"),
			insights:   read("insights.json"),
			llmContext: withoutClockLine(read("llm_context.md")),
			snapshotID: r.SnapshotID,
			hashes:     r.OutputHashes,
		}
	}
	for _, repo := range repos {
		if err := os.RemoveAll(eng.OutputDir(repo)); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// withoutClockLine drops llm_context.md's "Generated at <time> in <duration>"
// line and sorts the rest. The clock line differs between any two runs; the sort
// is because the renderer's route table orders two routes of one path in one
// file (GET and POST /widgets in openapi_sample) as the facts arrived, which
// varies run to run with or without this change. The facts do not vary.
func withoutClockLine(b []byte) []byte {
	var keep [][]byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("*Generated at ")) {
			keep = append(keep, line)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return bytes.Compare(keep[i], keep[j]) < 0 })
	return bytes.Join(keep, []byte("\n"))
}

// Over every golden fixture, a caller that declares its write (the id pass
// writes the facts file it hashes) and one that does not (the file is
// serialized again at write time) produce the same bytes, the same output
// hashes and the same snapshot id, member by member.
func TestWrittenOnce_GoldenFixturesAgreeEitherWay(t *testing.T) {
	for _, f := range fixtures {
		f := f
		t.Run(f.name, func(t *testing.T) {
			root := copyTree(t, f.sourceDir(), t.TempDir())
			configPath := f.configPath(t)
			serialized := walkAndWrite(t, f, root, configPath, false)
			written := walkAndWrite(t, f, root, configPath, true)
			for i := range serialized {
				a, b := serialized[i], written[i]
				if len(a.facts) == 0 || !bytes.Equal(a.facts, b.facts) {
					t.Errorf("member %d: facts.jsonl differs (%d vs %d bytes)", i, len(a.facts), len(b.facts))
				}
				if !bytes.Equal(a.insights, b.insights) {
					t.Errorf("member %d: insights.json differs", i)
				}
				if !bytes.Equal(a.llmContext, b.llmContext) {
					t.Errorf("member %d: llm_context.md holds different lines", i)
				}
				if a.snapshotID == "" || a.snapshotID != b.snapshotID {
					t.Errorf("member %d: snapshot id %q vs %q", i, a.snapshotID, b.snapshotID)
				}
				for _, name := range []string{"facts.jsonl", "insights.json"} {
					if a.hashes[name] == "" || a.hashes[name] != b.hashes[name] {
						t.Errorf("member %d: output hash of %s %q vs %q", i, name, a.hashes[name], b.hashes[name])
					}
				}
			}
		})
	}
}
