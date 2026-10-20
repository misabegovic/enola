package pythonextractor

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// pytestModule is pytest's default python_files: test_*.py and *_test.py. A
// conftest or a helper module under tests/ is read for its references and holds
// no test pytest would collect.
func pytestModule(relFile string) bool {
	base := path.Base(relFile)
	return strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.HasSuffix(base, "_test.py")
}

// pyTestCases names the tests pytest collects from one module by its default
// rules: module-level functions whose name starts with "test", and methods
// starting with "test" in classes that pytest collects, which are classes named
// Test* with no __init__ (nested ones included) and subclasses of a TestCase,
// which unittest's loader collects whatever their name. A class that sets
// __test__ = False is not collected. The runner name is pytest's node id without
// the file: Class::method, or the function alone. A parametrised row is a run of
// its function and gets no fact of its own, and a definition that a later one of
// the same name shadows is not collected unless the later one is a class that
// subclasses it (see liveDefinitions).
func pyTestCases(src []byte, relFile string) []facts.Fact {
	if !pytestModule(relFile) {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(python.Language())); err != nil {
		return nil
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()

	var out []facts.Fact
	var walk func(body *sitter.Node, groups []string)
	walk = func(body *sitter.Node, groups []string) {
		live := liveDefinitions(body, src)
		for i := uint(0); i < body.NamedChildCount(); i++ {
			def := definitionOf(body.NamedChild(i))
			if def == nil || !live[i] {
				continue
			}
			name := pyText(def.ChildByFieldName("name"), src)
			switch def.Kind() {
			case "function_definition":
				if !strings.HasPrefix(name, "test") {
					continue
				}
				runner := strings.Join(append(append([]string(nil), groups...), name), "::")
				endLine, col, endCol := tsutil.Span(def, src)
				out = append(out, facts.NewTestCase("python", relFile, groups, name, runner,
					int(def.StartPosition().Row)+1, endLine, col, endCol))
			case "class_definition":
				if !pytestCollectsClass(def, src) {
					continue
				}
				if cbody := def.ChildByFieldName("body"); cbody != nil {
					walk(cbody, append(append([]string(nil), groups...), name))
				}
			}
		}
	}
	walk(tree.RootNode(), nil)
	return lastOfEachRunnerName(out)
}

// liveDefinitions are the definitions of one body that pytest can still reach
// when the module is imported. A later definition of a name rebinds it, so only
// the last is held, except that a class rebound by a class of the same name that
// subclasses it (django's class TestIsBoundBehavior(TestIsBoundBehavior)) passes
// its tests on to it, and they run under the same name.
func liveDefinitions(body *sitter.Node, src []byte) map[uint]bool {
	byName := map[string][]uint{}
	defs := map[uint]*sitter.Node{}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		if def := definitionOf(body.NamedChild(i)); def != nil {
			name := pyText(def.ChildByFieldName("name"), src)
			byName[name] = append(byName[name], i)
			defs[i] = def
		}
	}
	live := map[uint]bool{}
	for name, at := range byName {
		live[at[len(at)-1]] = true
		for k := len(at) - 2; k >= 0; k-- {
			next := defs[at[k+1]]
			live[at[k]] = live[at[k+1]] && defs[at[k]].Kind() == "class_definition" &&
				next.Kind() == "class_definition" && subclassesByName(next, name, src)
		}
	}
	return live
}

func subclassesByName(class *sitter.Node, name string, src []byte) bool {
	supers := class.ChildByFieldName("superclasses")
	if supers == nil {
		return false
	}
	for i := uint(0); i < supers.NamedChildCount(); i++ {
		if pyText(supers.NamedChild(i), src) == name {
			return true
		}
	}
	return false
}

// lastOfEachRunnerName keeps the last test of each runner name: a class that
// rebinds its own name and redefines an inherited test runs its own.
func lastOfEachRunnerName(cases []facts.Fact) []facts.Fact {
	last := map[string]int{}
	for i, c := range cases {
		last[c.PropString("runner_name")] = i
	}
	kept := cases[:0]
	for i, c := range cases {
		if last[c.PropString("runner_name")] == i {
			kept = append(kept, c)
		}
	}
	return kept
}

// definitionOf is the function or class a statement defines, looking through
// decorators: pytest collects a decorated test, and its span is the definition
// itself, since the decorators are siblings of it in the grammar.
func definitionOf(n *sitter.Node) *sitter.Node {
	switch n.Kind() {
	case "function_definition", "class_definition":
		return n
	case "decorated_definition":
		return n.ChildByFieldName("definition")
	}
	return nil
}

func pytestCollectsClass(class *sitter.Node, src []byte) bool {
	name := pyText(class.ChildByFieldName("name"), src)
	unittest := false
	if supers := class.ChildByFieldName("superclasses"); supers != nil {
		for i := uint(0); i < supers.NamedChildCount(); i++ {
			base := pyText(supers.NamedChild(i), src)
			if strings.HasSuffix(base[strings.LastIndex(base, ".")+1:], "TestCase") {
				unittest = true
			}
		}
	}
	if !unittest && !strings.HasPrefix(name, "Test") {
		return false
	}
	body := class.ChildByFieldName("body")
	if body == nil {
		return false
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		stmt := body.NamedChild(i)
		if def := definitionOf(stmt); def != nil && def.Kind() == "function_definition" &&
			pyText(def.ChildByFieldName("name"), src) == "__init__" && !unittest {
			return false
		}
		if stmt.Kind() == "expression_statement" && strings.ReplaceAll(pyText(stmt, src), " ", "") == "__test__=False" {
			return false
		}
	}
	return true
}
