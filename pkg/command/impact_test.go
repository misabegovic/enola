package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// impactRepo writes a repository where main and b.Beta both call a.Alpha, and
// snapshots it the way --generate does: impact reads a snapshot, never makes one.
func impactRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":     "module example.com/impact\n\ngo 1.21\n",
		"main.go":    "package main\n\nimport (\n\t\"example.com/impact/pkg/a\"\n\t\"example.com/impact/pkg/b\"\n)\n\nfunc main() {\n\ta.Alpha()\n\tb.Beta()\n}\n",
		"pkg/a/a.go": "package a\n\nfunc Alpha() {}\n",
		"pkg/b/b.go": "package b\n\nimport \"example.com/impact/pkg/a\"\n\nfunc Beta() {\n\ta.Alpha()\n}\n",
	} {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restore := chdir(t, t.TempDir())
	defer restore()
	tgt := testRunner().resolveTarget(repo)
	if _, err := tgt.engine.GenerateSnapshot(context.Background(), tgt.repoPaths[0], false); err != nil {
		t.Fatal(err)
	}
	if err := tgt.engine.WriteArtifacts(tgt.repoPaths[0]); err != nil {
		t.Fatal(err)
	}
	return repo
}

func runImpactIn(t *testing.T, repo string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	restore := chdir(t, t.TempDir())
	defer restore()
	var out, errOut bytes.Buffer
	code = testRunner().runImpact(context.Background(), append(args, repo), &out, &errOut)
	return out.String(), errOut.String(), code
}

type impactDoc struct {
	Resolution *struct {
		Matched    string            `json:"matched"`
		Candidates []json.RawMessage `json:"candidates"`
	} `json:"resolution"`
	ByDepth map[string][]struct {
		Name  string `json:"name"`
		File  string `json:"file"`
		Line  int    `json:"line"`
		Depth int    `json:"depth"`
	} `json:"by_depth"`
	TotalDependents int `json:"total_dependents"`
	Stats           struct {
		Truncated bool `json:"truncated"`
	} `json:"stats"`
}

func decodeImpact(t *testing.T, stdout string) impactDoc {
	t.Helper()
	var doc impactDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout must be one JSON document and nothing else: %v\n%s", err, stdout)
	}
	return doc
}

func TestImpact_JSONLocatesEveryDependent(t *testing.T) {
	stdout, _, code := runImpactIn(t, impactRepo(t), "--json", "pkg/a.Alpha")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	doc := decodeImpact(t, stdout)
	if doc.TotalDependents != 2 {
		t.Errorf("total_dependents = %d, want 2 (main and Beta both call Alpha)", doc.TotalDependents)
	}
	where := map[string]string{}
	for _, n := range doc.ByDepth["1"] {
		if n.Depth != 1 || n.Line == 0 {
			t.Errorf("dependent %q: depth %d line %d, want depth 1 and a line", n.Name, n.Depth, n.Line)
		}
		where[n.Name] = n.File
	}
	if where["pkg/b.Beta"] != "pkg/b/b.go" {
		t.Errorf("Beta should be a depth-1 dependent in pkg/b/b.go; got %v", where)
	}
	if len(where) != 2 {
		t.Errorf("depth 1 = %v, want main and Beta", where)
	}
}

func TestImpact_SummaryCountsAndListLocates(t *testing.T) {
	repo := impactRepo(t)

	stdout, _, code := runImpactIn(t, repo, "pkg/a.Alpha")
	if code != 0 {
		t.Fatalf("summary exit = %d, want 0", code)
	}
	for _, want := range []string{"2 total dependents", "## Dependents by kind", "## By depth", "## Hotspot modules"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary should contain %q; got:\n%s", want, stdout)
		}
	}

	stdout, _, code = runImpactIn(t, repo, "--list", "--include-forward", "pkg/b.Beta")
	if code != 0 {
		t.Fatalf("list exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "## Depth 1 (1)\n\n- ..main (symbol) — main.go:") {
		t.Errorf("--list should name Beta's caller with its file and line; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# Forward dependencies of pkg/b.Beta") || !strings.Contains(stdout, "- pkg/a.Alpha (symbol) — pkg/a/a.go:") {
		t.Errorf("--include-forward should list what Beta depends on; got:\n%s", stdout)
	}
}

func TestImpact_NodeCapKeepsTheTotalExact(t *testing.T) {
	// The cap counts the target, so 2 leaves room for one dependent.
	stdout, _, code := runImpactIn(t, impactRepo(t), "--json", "--max-nodes", "2", "pkg/a.Alpha")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	doc := decodeImpact(t, stdout)
	listed := 0
	for _, nodes := range doc.ByDepth {
		listed += len(nodes)
	}
	if listed != 1 || doc.TotalDependents != 2 || !doc.Stats.Truncated {
		t.Errorf("listed %d, total %d, truncated %v; want 1 listed of an exact 2, flagged truncated",
			listed, doc.TotalDependents, doc.Stats.Truncated)
	}
}

// The summary says how many dependents the breakdown covers, which is what was
// kept under the cap, beside the exact total.
func TestImpact_SummaryFooterCountsWhatIsShown(t *testing.T) {
	stdout, _, code := runImpactIn(t, impactRepo(t), "--max-nodes", "2", "pkg/a.Alpha")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"2 total dependents (showing 1)", "- symbol: 1\n", "**2** total transitive dependents"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary should contain %q; got:\n%s", want, stdout)
		}
	}
}

// An empty list that exits 0 would tell a script the change is safe.
func TestImpact_UnresolvedTargetIsNotAnEmptyAnswer(t *testing.T) {
	repo := impactRepo(t)

	stdout, stderr, code := runImpactIn(t, repo, "--json", "a")
	if code != 2 {
		t.Errorf("ambiguous target: exit = %d, want 2", code)
	}
	doc := decodeImpact(t, stdout)
	if doc.Resolution == nil || doc.Resolution.Matched != "" || len(doc.Resolution.Candidates) == 0 {
		t.Errorf("the document must still carry the candidates to choose from; got:\n%s", stdout)
	}
	if len(doc.ByDepth) != 0 {
		t.Errorf("nothing was traversed, so nothing may be listed; got %v", doc.ByDepth)
	}
	if !strings.Contains(stderr, "nothing was traversed") {
		t.Errorf("stderr must say why; got:\n%s", stderr)
	}

	// The tool's error is the answer printed, as every twin prints it; the exit says it is one.
	stdout, _, code = runImpactIn(t, repo, "--json", "NoSuchSymbolAnywhere")
	if code != 2 || strings.TrimSpace(stdout) != `no facts matching "NoSuchSymbolAnywhere"` {
		t.Errorf("unmatched target: exit = %d stdout = %q, want 2 and the tool's miss", code, stdout)
	}
}

// Each of these would otherwise answer something other than what was asked.
func TestImpact_UsageErrors(t *testing.T) {
	repo := impactRepo(t)
	for name, args := range map[string][]string{
		"no target":                   nil,
		"--json with --list":          {"--json", "--list", "x"},
		"flag after the target":       {"pkg/a.Alpha", repo, "--json"},
		"unquoted scoped target":      {"kind:symbol", "Alpha", repo},
		"--max-depth out of range":    {"--max-depth", "50", "x"},
		"--max-nodes out of range":    {"--max-nodes", "0", "x"},
		"--max-depth zero, not unset": {"--max-depth", "0", "x"},
	} {
		var out, errOut bytes.Buffer
		if code := testRunner().runImpact(context.Background(), args, &out, &errOut); code != 2 {
			t.Errorf("%s: exit = %d, want 2", name, code)
		}
		if out.Len() != 0 {
			t.Errorf("%s: a usage error writes nothing to stdout; got %q", name, out.String())
		}
	}
}

// A review starts from a diff, which names files. A file is answered from the
// symbols it declares, and the report says that is what it did.
func TestImpact_FilePathReportsItsSymbols(t *testing.T) {
	stdout, _, code := runImpactIn(t, impactRepo(t), "--list", "pkg/a/a.go")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout:\n%s", code, stdout)
	}
	for _, want := range []string{"pkg/a/a.go is a file, not a node: this is the impact of the", "- pkg/b.Beta (symbol) — pkg/b/b.go:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--list on a file should contain %q; got:\n%s", want, stdout)
		}
	}
}
