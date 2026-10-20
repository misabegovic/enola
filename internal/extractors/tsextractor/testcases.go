package tsextractor

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// jsTestGroups and jsTests are the describe/it vocabulary jest, vitest, mocha and
// node's own test runner share. context and suite are mocha's spellings of a group,
// specify its spelling of a test.
var jsTestGroups = map[string]bool{"describe": true, "context": true, "suite": true}
var jsTests = map[string]bool{"it": true, "test": true, "specify": true}

// jsTestModifiers select or mark a test without changing its name; each is a
// member of the group or test function (it.only, describe.skip).
var jsTestModifiers = map[string]bool{"only": true, "skip": true, "todo": true, "concurrent": true, "failing": true, "sequential": true}

// isJSTestFile is the naming jest's and vitest's defaults select: a .test. or
// .spec. file of any JavaScript or TypeScript flavour, or a file under
// __tests__. A fixture tree is not a suite even when it holds test-shaped code,
// and Ember's tests/**/*-test files are QUnit modules, whose names are built
// another way.
func isJSTestFile(relFile string) bool {
	segments := strings.Split(relFile, "/")
	for _, seg := range segments[:len(segments)-1] {
		switch seg {
		case "fixtures", "fixture", "__fixtures__", "testdata", "node_modules":
			return false
		}
	}
	base := path.Base(relFile)
	ext := path.Ext(base)
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts":
	default:
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	for _, seg := range segments[:len(segments)-1] {
		if seg == "__tests__" {
			return true
		}
	}
	return false
}

type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
}

// tsTestCases names the tests a describe/it file defines, each by the full name
// jest reports and filters on with -t: the labels of its enclosing groups and its
// own, joined by spaces. The span is the call, from it or test to its closing
// parenthesis. A label that is not a literal (a template with a substitution, an
// escape, a variable) gets no fact and is counted, and so is every test of a
// table (.each), whose names are built from its rows.
func tsTestCases(kinds *tsutil.KindTable, root *sitter.Node, src []byte, relFile string) testCaseReading {
	r := &tsTestReader{kinds: kinds, src: src, relFile: relFile}
	r.walk(root, nil)
	return testCaseReading{cases: r.out, unlabelled: r.unlabelled}
}

type tsTestReader struct {
	kinds      *tsutil.KindTable
	src        []byte
	relFile    string
	out        []facts.Fact
	unlabelled int
}

func (r *tsTestReader) walk(node *sitter.Node, groups []string) {
	if node == nil {
		return
	}
	if r.kinds.Of(node) == "call_expression" {
		switch _, table, shape := r.testCall(node); shape {
		case testCallGroup:
			r.enterGroup(node, groups, table)
			return
		case testCallTest:
			r.emitTest(node, groups, table)
			return
		case testCallOpaque:
			// A DSL member this reader does not know (test.extend, test.use) may
			// change what the tests under it are called, so they are counted
			// rather than named without it.
			r.unlabelled += r.countTests(r.callback(node))
			return
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		r.walk(node.NamedChild(i), groups)
	}
}

const (
	testCallNone = iota
	testCallGroup
	testCallTest
	testCallOpaque
)

// testCall reads the callee of describe(...), it.only(...), test.each(...)(...)
// and Playwright's test.describe(...): the DSL word, whether it is a table, and
// whether the call opens a group, is a test, or is a DSL member the reader does
// not know.
func (r *tsTestReader) testCall(call *sitter.Node) (string, bool, int) {
	fn := call.ChildByFieldName("function")
	table := false
	var props []string
	// The callee is a chain of members and calls ending in the DSL word:
	// describe.each(rows)(label, fn) calls through the table, and
	// test.extend(fixtures).describe(label, fn) through a member that is not one.
chain:
	for fn != nil {
		switch r.kinds.Of(fn) {
		case "member_expression":
			props = append(props, nodeText(fn.ChildByFieldName("property"), r.src))
			fn = fn.ChildByFieldName("object")
		case "call_expression":
			fn = fn.ChildByFieldName("function")
		default:
			break chain
		}
	}
	if fn == nil || r.kinds.Of(fn) != "identifier" {
		return "", false, testCallNone
	}
	word := nodeText(fn, r.src)
	if !jsTestGroups[word] && !jsTests[word] {
		return "", false, testCallNone
	}
	group := jsTestGroups[word]
	for _, prop := range props {
		switch {
		case prop == "each":
			table = true
		case prop == "describe" && word == "test":
			group = true
		case jsTestModifiers[prop] || prop == "serial" || prop == "parallel" || prop == "fixme":
		default:
			return word, table, testCallOpaque
		}
	}
	if group {
		return word, table, testCallGroup
	}
	return word, table, testCallTest
}

func (r *tsTestReader) enterGroup(call *sitter.Node, groups []string, table bool) {
	label, labelled := r.label(call)
	body := r.callback(call)
	if table || !labelled {
		r.unlabelled += r.countTests(body)
		return
	}
	next := append(append([]string(nil), groups...), label)
	if body != nil {
		r.walk(body, next)
	}
}

func (r *tsTestReader) emitTest(call *sitter.Node, groups []string, table bool) {
	label, labelled := r.label(call)
	if table || !labelled {
		r.unlabelled++
		return
	}
	runner := strings.Join(append(append([]string(nil), groups...), label), " ")
	endLine, col, endCol := tsutil.Span(call, r.src)
	r.out = append(r.out, facts.NewTestCase("typescript", r.relFile, groups, label, runner,
		int(call.StartPosition().Row)+1, endLine, col, endCol))
}

// label is a call's first argument when it is a literal: a quoted string or a
// template with no substitution, holding no escape.
func (r *tsTestReader) label(call *sitter.Node) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	first := args.NamedChild(0)
	switch r.kinds.Of(first) {
	case "string", "template_string":
		var b strings.Builder
		for i := uint(0); i < first.NamedChildCount(); i++ {
			part := first.NamedChild(i)
			if r.kinds.Of(part) != "string_fragment" {
				return "", false
			}
			b.WriteString(nodeText(part, r.src))
		}
		return b.String(), true
	}
	return "", false
}

// callback is the body of the function a group or test is given, for the walk
// into a group.
func (r *tsTestReader) callback(call *sitter.Node) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		switch r.kinds.Of(arg) {
		case "arrow_function", "function_expression", "function":
			return arg.ChildByFieldName("body")
		}
	}
	return nil
}

func (r *tsTestReader) countTests(node *sitter.Node) int {
	if node == nil {
		return 0
	}
	n := 0
	if r.kinds.Of(node) == "call_expression" {
		if _, _, shape := r.testCall(node); shape == testCallTest {
			n++
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		n += r.countTests(node.NamedChild(i))
	}
	return n
}
