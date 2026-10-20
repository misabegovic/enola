package facts

import (
	"sort"
	"testing"
)

// A file declares facts; what imports or calls them depends on the file. The
// impact of changing a file named by its path is what depends on anything it
// declares, the way a type's impact reaches what calls its methods.
func TestImpactSet_AFilePathReachesWhatDependsOnItsFacts(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindSymbol, Name: "src/parser.OutputParser", File: "src/parser/mod.rs", Repo: "crate", Props: map[string]any{"symbol_kind": SymbolStruct}},
		Fact{Kind: KindSymbol, Name: "src/parser.parse", File: "src/parser/mod.rs", Props: map[string]any{"symbol_kind": "function"},
			Relations: []Relation{{Kind: RelCalls, Target: "src/parser.helper"}}},
		Fact{Kind: KindSymbol, Name: "src/parser.helper", File: "src/parser/mod.rs", Props: map[string]any{"symbol_kind": "function"}},
		Fact{Kind: KindDependency, Name: "src/cmds -> crate::parser::OutputParser", File: "src/cmds/a.rs",
			Relations: []Relation{{Kind: RelImports, Target: "src/parser.OutputParser"}}},
		Fact{Kind: KindSymbol, Name: "src/cmds.run_b", File: "src/cmds/b.rs", Repo: "crate", Props: map[string]any{"symbol_kind": "function"},
			Relations: []Relation{{Kind: RelCalls, Target: "src/parser.parse"}}},
		Fact{Kind: KindSymbol, Name: "src/main.main", File: "src/main.rs", Props: map[string]any{"symbol_kind": "function"},
			Relations: []Relation{{Kind: RelCalls, Target: "src/cmds.run_b"}}},
		Fact{Kind: KindSymbol, Name: "src/other.unrelated", File: "src/other.rs", Props: map[string]any{"symbol_kind": "function"}},
	)
	s.BuildGraph()
	g := s.Graph()

	result := g.ImpactSet("src/parser/mod.rs", 3, 100, false)
	var depth1, depth2 []string
	for _, n := range result.ByDepth[1] {
		depth1 = append(depth1, n.Name)
	}
	for _, n := range result.ByDepth[2] {
		depth2 = append(depth2, n.Name)
	}
	sort.Strings(depth1)
	want1 := []string{"src/cmds -> crate::parser::OutputParser", "src/cmds.run_b"}
	if len(depth1) != len(want1) || depth1[0] != want1[0] || depth1[1] != want1[1] {
		t.Errorf("depth 1 = %v, want %v: what imports or calls a fact the file declares", depth1, want1)
	}
	if len(depth2) != 1 || depth2[0] != "src/main.main" {
		t.Errorf("depth 2 = %v, want [src/main.main]", depth2)
	}
	if len(result.CrossRepoImpact) > 0 {
		t.Errorf("cross-repo impact = %v, want none: the file and its dependents share one repository", result.CrossRepoImpact)
	}
	if result.TotalDependents != 3 {
		t.Errorf("total dependents = %d, want 3; the file's own facts are the seeds, not dependents", result.TotalDependents)
	}
	for _, nodes := range result.ByDepth {
		for _, n := range nodes {
			if n.Name == "src/parser.helper" || n.Name == "src/other.unrelated" {
				t.Errorf("%s is inside the file or unrelated to it and must not be a dependent", n.Name)
			}
		}
	}
}

// A name that is no file's path keeps the reading it had.
func TestImpactSet_ANameThatIsNoFileIsUnchanged(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindSymbol, Name: "pkg.A", File: "pkg/a.go", Relations: []Relation{{Kind: RelCalls, Target: "pkg.B"}}},
		Fact{Kind: KindSymbol, Name: "pkg.B", File: "pkg/b.go"},
	)
	s.BuildGraph()
	g := s.Graph()
	if got := g.ImpactSet("pkg.B", 3, 100, false).TotalDependents; got != 1 {
		t.Errorf("dependents of pkg.B = %d, want 1", got)
	}
	if got := g.ImpactSet("pkg/nothing.go", 3, 100, false).TotalDependents; got != 0 {
		t.Errorf("a path no fact lives in has %d dependents, want 0", got)
	}
}
