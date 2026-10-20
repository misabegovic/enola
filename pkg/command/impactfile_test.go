package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rustCrate writes a crate whose parser module is imported by two command
// files and called from one of them.
func rustCrate(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for rel, content := range map[string]string{
		"Cargo.toml":        "[package]\nname = \"crate\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/main.rs":       "mod cmds;\nmod parser;\n\nfn main() {\n    cmds::a::run();\n}\n",
		"src/parser/mod.rs": "pub struct OutputParser;\n\nimpl OutputParser {\n    pub fn parse(&self) -> usize {\n        0\n    }\n}\n",
		"src/cmds/mod.rs":   "pub mod a;\npub mod b;\n",
		"src/cmds/a.rs":     "use crate::parser::OutputParser;\n\npub fn run() -> usize {\n    let p = OutputParser;\n    p.parse()\n}\n",
		"src/cmds/b.rs":     "use crate::parser::OutputParser;\n\npub fn describe(_p: &OutputParser) -> &'static str {\n    \"parser\"\n}\n",
		"src/other/mod.rs":  "pub fn unrelated() {}\n",
	} {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// impact on a Rust file's path reaches every file that imports what the file
// declares. Before, the walk started at the path, which no edge names, and
// answered that nothing depends on it; and the two importers' dependency
// facts share one name, so the node reached names both files.
func TestImpactOnARustFileReachesItsImporters(t *testing.T) {
	a := &answersRun{t: t, bin: answersBin(t), repo: rustCrate(t), home: t.TempDir()}
	a.mustGenerate()

	code, out, errOut := a.run("impact", "--json", "src/parser/mod.rs", ".")
	if code != 0 {
		t.Fatalf("impact --json exit %d\n%s\n%s", code, out, errOut)
	}
	var result struct {
		TotalDependents int `json:"total_dependents"`
		ByDepth         map[string][]struct {
			Name  string   `json:"name"`
			File  string   `json:"file"`
			Files []string `json:"files"`
		} `json:"by_depth"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("impact --json: %v\n%s", err, out)
	}
	files := map[string]bool{}
	for _, nodes := range result.ByDepth {
		for _, n := range nodes {
			files[n.File] = true
			for _, f := range n.Files {
				files[f] = true
			}
		}
	}
	var got []string
	for f := range files {
		got = append(got, f)
	}
	sort.Strings(got)
	for _, want := range []string{"src/cmds/a.rs", "src/cmds/b.rs"} {
		if !files[want] {
			t.Errorf("impact of src/parser/mod.rs misses %s, which imports OutputParser; dependents' files: %v", want, got)
		}
	}
	if files["src/other/mod.rs"] || files["src/parser/mod.rs"] {
		t.Errorf("impact lists the file itself or an unrelated one: %v", got)
	}
	if result.TotalDependents == 0 {
		t.Error("total_dependents = 0")
	}

	_, summary, _ := a.run("impact", "src/parser/mod.rs", ".")
	if strings.Contains(summary, "No dependents found") {
		t.Errorf("the summary still says nothing depends on the file:\n%s", summary)
	}
}
