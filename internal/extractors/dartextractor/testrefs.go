package dartextractor

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/parallel"
)

// ExtractTestRefs implements plugin.TestRefExtractor.
//
// Test files are excluded from the production graph, which is right — a test class is
// not architecture, and indexing one makes every test helper a dead-code candidate. But
// excluding them outright creates the opposite error: a production symbol whose only
// caller is its test then reads as unreferenced, and the dead-code detector accuses it.
//
// This pass keeps only the outbound half. It emits reference-only facts carrying
// `calls` relations to the production names each test touches, and no symbols at all,
// so a test can vouch for production code without ever becoming architecture itself.
//
// Dart makes this cheap and unusually accurate, because a test's imports name exactly
// which library it exercises.
func (e *DartExtractor) ExtractTestRefs(ctx context.Context, repoPath string, testFiles, _ []string) ([]facts.Fact, error) {
	var dartTests []string
	for _, f := range testFiles {
		if isDartFile(f) && !isGeneratedDart(f) {
			dartTests = append(dartTests, f)
		}
	}
	if len(dartTests) == 0 {
		return nil, nil
	}

	type testFile struct {
		ref   facts.Fact
		cases testCaseReading
	}
	out := parallel.MapFiles(ctx, dartTests, func(relFile string) testFile {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			return testFile{}
		}
		ref, cases := extractTestRefsAndCases(src, relFile)
		return testFile{ref: ref, cases: cases}
	})

	var result []facts.Fact
	named, unlabelled := 0, 0
	for _, tf := range out {
		if tf.ref.Kind != "" {
			result = append(result, tf.ref)
		}
		result = append(result, tf.cases.cases...)
		named += len(tf.cases.cases)
		unlabelled += tf.cases.unlabelled
	}
	if unlabelled > 0 {
		if f, ok := extcoverage.Fact(repoPath, "dart:test_cases", "test_case", named, map[string]int{"label_not_literal": unlabelled}); ok {
			result = append(result, f)
		}
	}
	return result, nil
}

// extractTestRefs collects the production names one test file references.
func extractTestRefs(src []byte, relFile string) facts.Fact {
	ref, _ := extractTestRefsAndCases(src, relFile)
	return ref
}

// extractTestRefsAndCases reads one test file's references and its tests from one
// parse.
func extractTestRefsAndCases(src []byte, relFile string) (facts.Fact, testCaseReading) {
	pp, _ := parserPool.Get().(*sitter.Parser)
	if pp == nil {
		return facts.Fact{}, testCaseReading{}
	}
	defer parserPool.Put(pp)
	tree := pp.Parse(src, nil)
	if tree == nil {
		return facts.Fact{}, testCaseReading{}
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		return facts.Fact{}, testCaseReading{}
	}
	cases := dartTestCases(root, src, relFile)
	return dartTestRef(root, src, relFile), cases
}

func dartTestRef(root *sitter.Node, src []byte, relFile string) facts.Fact {

	targets := map[string]bool{}
	var visit func(*sitter.Node)
	visit = func(n *sitter.Node) {
		kids := namedChildren(n)
		for i, c := range kids {
			switch kindOf(c) {
			case "const_object_expression", "new_expression":
				if t := childOfKind(c, "type_identifier"); t != nil {
					addTestTarget(targets, t.Utf8Text(src))
				}
			case "selector":
				if childOfKind(c, "argument_part") == nil {
					continue
				}
				name, receiver := calleeNameAt(kids, i, src)
				if name == "" {
					continue
				}
				addTestTarget(targets, name)
				if receiver != "" && receiver != name && isUpper(receiver[0]) {
					addTestTarget(targets, receiver)
					addTestTarget(targets, receiver+"."+name)
				}
			}
		}
		for _, c := range kids {
			visit(c)
		}
	}
	visit(root)

	if len(targets) == 0 {
		return facts.Fact{}
	}
	names := make([]string, 0, len(targets))
	for t := range targets {
		names = append(names, t)
	}
	sort.Strings(names)
	rels := make([]facts.Relation, 0, len(names))
	for _, t := range names {
		rels = append(rels, facts.Relation{Kind: facts.RelCalls, Target: t})
	}
	return facts.Fact{
		Kind:      facts.KindTestRef,
		Name:      relFile,
		File:      relFile,
		Props:     map[string]any{"language": "dart"},
		Relations: rels,
	}
}

// testHarnessNames are the test framework's own vocabulary, dropped INCLUDING the bare
// method name.
//
// Filtering only the qualified form is the bug the C# extractor documents: dropping
// `Assert.Equal` while letting `Equal` through means the harness vouches for a
// production symbol named `Equal` that no test exercises. Dart's harness words are
// especially dangerous here — `test`, `group`, `expect`, `setUp` and `find` are all
// ordinary application vocabulary, and `find` in particular is a method half the
// repositories in the corpus declare.
var testHarnessNames = map[string]bool{
	"test": true, "testWidgets": true, "group": true, "setUp": true, "setUpAll": true,
	"tearDown": true, "tearDownAll": true, "expect": true, "expectLater": true,
	"fail": true, "skip": true, "equals": true, "isNull": true, "isNotNull": true,
	"isTrue": true, "isFalse": true, "throwsA": true, "anyOf": true, "allOf": true,
	"pumpWidget": true, "pumpAndSettle": true, "find": true, "byType": true,
	"byKey": true, "byIcon": true, "text": true, "widgetWithText": true,
	"when": true, "verify": true, "verifyNever": true, "any": true, "argThat": true,
	"thenAnswer": true, "thenReturn": true, "thenThrow": true, "reset": true,
	"registerFallbackValue": true, "mock": true, "returnsNormally": true,
	"completes": true, "emits": true, "emitsInOrder": true, "predicate": true,
	"contains": true, "hasLength": true, "isA": true, "same": true, "print": true,
}

func addTestTarget(targets map[string]bool, name string) {
	if name == "" || testHarnessNames[name] {
		return
	}
	// A mock class is the test's own construction, not a production reference.
	if strings.HasPrefix(name, "Mock") || strings.HasPrefix(name, "Fake") ||
		strings.HasPrefix(name, "_") {
		return
	}
	targets[name] = true
}

// calleeNameAt is the src-only half of walker.calleeOf, for the test pass which has no
// walker (it emits no symbols and needs no package context).
func calleeNameAt(kids []*sitter.Node, i int, src []byte) (name, receiver string) {
	if i == 0 {
		return "", ""
	}
	prev := kids[i-1]
	if kindOf(prev) == "selector" {
		sel := childOfKind(prev, "unconditional_assignable_selector", "conditional_assignable_selector")
		if sel == nil {
			return "", ""
		}
		name = identifierChild(sel, src)
		base := ""
		for j := i - 2; j >= 0; j-- {
			if kindOf(kids[j]) == "selector" {
				continue
			}
			base = strings.TrimSpace(kids[j].Utf8Text(src))
			break
		}
		if base != "" && isPlainIdentifier(base) {
			return name, base
		}
		return name, ""
	}
	base := strings.TrimSpace(prev.Utf8Text(src))
	if base == "" || !isPlainIdentifier(base) {
		return "", ""
	}
	return base, base
}
