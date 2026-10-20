package facts

import "testing"

func depCarrying(repo, file, targetFile, target string) Fact {
	f := Fact{Kind: KindDependency, Name: file + " -> " + target, File: file, Repo: repo,
		Relations: []Relation{{Kind: RelImports, Target: target}}}
	if targetFile != "" {
		f.Props = map[string]any{PropTargetFile: targetFile}
	}
	return f
}

func symbolAt(repo, name, file string) Fact {
	return Fact{Kind: KindSymbol, Name: name, File: file, Repo: repo}
}

func TestImportTargets_AnItemTargetLandsOnItsFilesDirectory(t *testing.T) {
	ff := []Fact{symbolAt("r", "src/core.count", "src/core/count.rs"), symbolAt("r", "src/core/deep.count", "src/core/deep/count.rs")}
	targets := NewImportTargets(func() []Fact { return ff })
	dir, carried := targets.Dir(depCarrying("r", "src/hooks/run.rs", "src/core/count.rs", "src/core.count"), "src/core.count")
	if !carried || dir != "src/core" {
		t.Fatalf("want src/core, carried; got %q, %v", dir, carried)
	}
}

// A Rust shared name is several declarations, each in one file, under one
// fact name; the carried file says which of them the import names.
func TestImportTargets_ASharedNameResolvesByTheCarriedFile(t *testing.T) {
	ff := []Fact{symbolAt("r", "src.run", "src/a.rs"), symbolAt("r", "src.run", "src/b.rs"), symbolAt("r", "src.run", "lib/c.rs")}
	targets := NewImportTargets(func() []Fact { return ff })
	if dir, carried := targets.Dir(depCarrying("r", "x/main.rs", "lib/c.rs", "src.run"), "src.run"); !carried || dir != "lib" {
		t.Fatalf("want lib, carried; got %q, %v", dir, carried)
	}
}

func TestImportTargets_ANameNotDeclaredAtTheCarriedFileIsRefused(t *testing.T) {
	ff := []Fact{symbolAt("r", "src.run", "src/a.rs"), symbolAt("other", "src.run", "src/c.rs")}
	targets := NewImportTargets(func() []Fact { return ff })
	if dir, carried := targets.Dir(depCarrying("r", "x/main.rs", "src/c.rs", "src.run"), "src.run"); !carried || dir != "" {
		t.Fatalf("want a refusal (no directory), carried; got %q, %v", dir, carried)
	}
}

// No producer but Rust carries a file on a fact holding imports, so every
// other import keeps the reader's own walk.
func TestImportTargets_WithoutACarriedFileTheReaderKeepsItsWalk(t *testing.T) {
	loaded := false
	targets := NewImportTargets(func() []Fact { loaded = true; return nil })
	if dir, carried := targets.Dir(depCarrying("r", "x/main.rs", "", "src/core"), "src/core"); carried || dir != "" {
		t.Fatalf("want not carried; got %q, %v", dir, carried)
	}
	if loaded {
		t.Fatal("the symbol index is built only when a carried file needs it")
	}
}

// In an append-mode snapshot a fact's file carries its repository prefix
// while target_file stays repository-relative, the case memberAtCarriedFile
// handles the same way.
func TestImportTargets_RepoPrefixedFilesMatchARepoRelativeTargetFile(t *testing.T) {
	ff := []Fact{symbolAt("svc", "src/core.count", "svc/src/core/count.rs")}
	targets := NewImportTargets(func() []Fact { return ff })
	if dir, carried := targets.Dir(depCarrying("svc", "svc/src/hooks/run.rs", "src/core/count.rs", "src/core.count"), "src/core.count"); !carried || dir != "src/core" {
		t.Fatalf("want src/core, carried; got %q, %v", dir, carried)
	}
}

// Store.Freeze shares relation slices between facts by kind and target, so
// two calls dependencies to one shared name must differ by name, or the
// identity that keeps them apart is lost.
func TestCallByFileName_TwoTargetFilesStayTwoFactsAfterFreeze(t *testing.T) {
	mk := func(targetFile string) Fact {
		return Fact{Kind: KindDependency, Name: CallByFileName("src/core.rs", "src.run", targetFile), File: "src/core.rs",
			Repo: "r", Props: map[string]any{PropTargetFile: targetFile},
			Relations: []Relation{{Kind: RelCalls, Target: "src.run"}}}
	}
	s := NewStore()
	s.Add(mk("src/a.rs"), mk("src/b.rs"), mk("src/a.rs"))
	s.Freeze()
	byFile := map[string]int{}
	for _, f := range s.ByKind(KindDependency) {
		byFile[f.PropString(PropTargetFile)]++
	}
	if len(byFile) != 2 || byFile["src/a.rs"] == 0 || byFile["src/b.rs"] == 0 {
		t.Fatalf("want facts for both target files, got %v", byFile)
	}
	if a, b := CallByFileName("src/core.rs", "src.run", "src/a.rs"), CallByFileName("src/core.rs", "src.run", "src/b.rs"); a == b {
		t.Fatalf("names must differ by target file: %q", a)
	}
}

func itemImportFacts() []Fact {
	return []Fact{
		{Kind: KindModule, Name: "src", File: "src"},
		{Kind: KindModule, Name: "src/core", File: "src/core"},
		{Kind: KindModule, Name: "src/hooks", File: "src/hooks"},
		{Kind: KindModule, Name: "src/cli", File: "src/cli"},
		{Kind: KindSymbol, Name: "src/core.count", File: "src/core/count.rs"},
		depCarrying("", "src/hooks/run.rs", "src/core/count.rs", "src/core.count"),
		depCarrying("", "src/cli/main.rs", "src/core/other.rs", "src/core.count"),
		depCarrying("", "src/cli/main.rs", "", "src/hooks"),
	}
}

// The graph's module-to-module import edges read an item target by its file.
func TestGraph_ItemImportTargetBecomesAModuleEdgeToItsFilesModule(t *testing.T) {
	g := NewGraph(itemImportFacts())
	edgesTo := func(module string) []string {
		var out []string
		for _, e := range g.ForwardEdges(module) {
			if e.RelKind == RelImports {
				out = append(out, e.Target)
			}
		}
		return out
	}
	if got := edgesTo("src/hooks"); len(got) != 1 || got[0] != "src/core" {
		t.Errorf("src/hooks imports = %v, want [src/core]", got)
	}
	if got := edgesTo("src/cli"); len(got) != 1 || got[0] != "src/hooks" {
		t.Errorf("src/cli imports = %v, want [src/hooks] (the refused item leaves no edge)", got)
	}
	// An item target is one fact, so the node an imports edge reaches is
	// that symbol, picked by the single-fact rule.
	id, ok := g.lookup("src/core.count")
	if !ok {
		t.Fatal("no node for the item target")
	}
	if idx, found := g.factIndexForID(id, RelImports); !found || g.facts[idx].Kind != KindSymbol {
		t.Errorf("the imports edge's target node must read as the symbol fact")
	}
}
