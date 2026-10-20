package rubyextractor

import (
	"path"
	"regexp"
	"strings"

	ruby "github.com/enola-labs/enola/internal/extractors/rubyextractor/grammar"
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// rspecGroups and rspecExamples are the RSpec DSL words that open an example group
// and an example, with their x- and f- forms, which still define the example (a
// skipped or focused one is listed by --dry-run like any other).
var rspecGroups = map[string]bool{
	"describe": true, "context": true, "feature": true, "example_group": true,
	"xdescribe": true, "xcontext": true, "xfeature": true, "fdescribe": true, "fcontext": true, "ffeature": true,
}

var rspecExamples = map[string]bool{
	"it": true, "specify": true, "example": true, "scenario": true, "its": false,
	"xit": true, "xspecify": true, "xexample": true, "xscenario": true,
	"fit": true, "fspecify": true, "fexample": true, "fscenario": true,
	"pending": true, "skip": false,
}

// rspecShared blocks hold examples that run only where another group includes
// them, so they are not tests of this file.
var rspecShared = map[string]bool{"shared_examples": true, "shared_examples_for": true, "shared_context": true}

// testCaseReading is what one file gave: the test cases, and how many tests had a
// label that is not a literal and so got no fact.
type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
}

// rubyTestCases names the tests a Ruby test file defines. In a *_spec.rb file they
// are RSpec examples, named by the full description `rspec --dry-run` prints: each
// group's description joined to its parent's with a space, or with none when the
// parent was described by a constant and the child starts with #, . or ::, then the
// example's own description after a space. In any other file they are minitest's:
// public test_* methods and Rails `test "label"` blocks in a class whose superclass
// is a Test or TestCase, named Class#test_name as minitest prints it, with a
// block's label turned into its method name the way ActiveSupport does. A
// description or label that is not a literal gets no fact and is counted.
func rubyTestCases(src []byte, relFile string) testCaseReading {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(ruby.Language())); err != nil {
		return testCaseReading{}
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()
	return rubyTestCasesTree(tree, src, relFile)
}

// rubyTestFile is what the reference-only pass reads from one test file: its
// test_ref and its test cases, from one parse.
func rubyTestFile(src []byte, relFile string) ([]facts.Fact, testCaseReading) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(ruby.Language())); err != nil {
		return nil, testCaseReading{}
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()
	return refsFromRubyTree(tree, src, relFile, facts.KindTestRef), rubyTestCasesTree(tree, src, relFile)
}

func rubyTestCasesTree(tree *sitter.Tree, src []byte, relFile string) testCaseReading {
	r := &rubyTestReader{src: src, relFile: relFile}
	if strings.HasSuffix(path.Base(relFile), "_spec.rb") {
		r.walkSpec(tree.RootNode(), nil)
	} else {
		r.walkMinitest(tree.RootNode(), nil)
	}
	return testCaseReading{cases: r.out, unlabelled: r.unlabelled}
}

type rubyTestReader struct {
	src        []byte
	relFile    string
	out        []facts.Fact
	unlabelled int
}

// specGroup is one enclosing example group: its own description, its full one,
// and whether it was described by a constant, which decides the separator.
type specGroup struct {
	own, full string
	constant  bool
}

func (r *rubyTestReader) walkSpec(node *sitter.Node, groups []specGroup) {
	if node == nil {
		return
	}
	if kindOf(node) == "call" {
		if method := node.ChildByFieldName("method"); method != nil && r.dslReceiver(node) {
			word := rubyText(method, r.src)
			switch {
			case rspecShared[word]:
				return
			case rspecGroups[word]:
				r.enterSpecGroup(node, groups)
				return
			case rspecExamples[word] && len(groups) > 0:
				r.emitExample(node, groups)
				return
			}
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		r.walkSpec(node.NamedChild(i), groups)
	}
}

// dslReceiver accepts the bare DSL and RSpec.describe, the two spellings a spec
// opens a group with.
func (r *rubyTestReader) dslReceiver(call *sitter.Node) bool {
	recv := call.ChildByFieldName("receiver")
	return recv == nil || rubyText(recv, r.src) == "RSpec"
}

func (r *rubyTestReader) enterSpecGroup(call *sitter.Node, groups []specGroup) {
	args := descriptionArgs(call)
	block := call.ChildByFieldName("block")
	own, _, ok := r.groupDescription(args)
	if !ok {
		r.unlabelled += r.countExamples(block)
		return
	}
	full := own
	if len(groups) > 0 {
		parent := groups[len(groups)-1]
		sep := " "
		if parent.constant && len(args) > 0 && startsLikeAMember(r.literal(args[0])) {
			sep = ""
		}
		full = parent.full + sep + own
	}
	next := append(append([]specGroup(nil), groups...), specGroup{own: own, full: full, constant: len(args) > 0 && isConstantNode(args[len(args)-1])})
	if block != nil {
		for i := uint(0); i < block.NamedChildCount(); i++ {
			r.walkSpec(block.NamedChild(i), next)
		}
	}
}

// groupDescription is RSpec's description of a group from its first two
// arguments: a constant is its written name, a symbol its name, a string its text,
// and a second argument joins the first with no space when the first is a constant
// and the second starts with #, . or ::.
func (r *rubyTestReader) groupDescription(args []*sitter.Node) (string, bool, bool) {
	if len(args) == 0 || len(args) > 2 {
		return "", false, false
	}
	first, ok := r.descriptionPart(args[0])
	if !ok {
		return "", false, false
	}
	constant := isConstantNode(args[0])
	if len(args) == 1 {
		return first, constant, true
	}
	second, ok := r.descriptionPart(args[1])
	if !ok {
		return "", false, false
	}
	if constant && startsLikeAMember(second) {
		return first + second, constant, true
	}
	return first + " " + second, constant, true
}

func (r *rubyTestReader) emitExample(call *sitter.Node, groups []specGroup) {
	args := descriptionArgs(call)
	if len(args) != 1 {
		r.unlabelled++
		return
	}
	label, ok := r.descriptionPart(args[0])
	if !ok || isConstantNode(args[0]) {
		r.unlabelled++
		return
	}
	own := make([]string, len(groups))
	for i, g := range groups {
		own[i] = g.own
	}
	r.emit(call, own, label, groups[len(groups)-1].full+" "+label)
}

// countExamples counts the examples under a group whose own description is not a
// literal: none of them can be named.
func (r *rubyTestReader) countExamples(node *sitter.Node) int {
	if node == nil {
		return 0
	}
	n := 0
	if kindOf(node) == "call" {
		if method := node.ChildByFieldName("method"); method != nil && rspecExamples[rubyText(method, r.src)] {
			n++
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		n += r.countExamples(node.NamedChild(i))
	}
	return n
}

var minitestSuper = regexp.MustCompile(`(Test|TestCase)$`)

func (r *rubyTestReader) walkMinitest(node *sitter.Node, scopes []string) {
	if node == nil {
		return
	}
	switch kindOf(node) {
	case "module":
		name := rubyText(node.ChildByFieldName("name"), r.src)
		if body := node.ChildByFieldName("body"); body != nil && name != "" {
			r.walkMinitest(body, append(append([]string(nil), scopes...), name))
		}
		return
	case "class":
		name := rubyText(node.ChildByFieldName("name"), r.src)
		super := node.ChildByFieldName("superclass")
		inner := append(append([]string(nil), scopes...), name)
		if body := node.ChildByFieldName("body"); body != nil && name != "" {
			if super != nil && minitestSuper.MatchString(strings.TrimSpace(strings.TrimPrefix(rubyText(super, r.src), "<"))) {
				r.readMinitestClass(body, inner)
			}
			r.walkMinitest(body, inner)
		}
		return
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		r.walkMinitest(node.NamedChild(i), scopes)
	}
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func (r *rubyTestReader) readMinitestClass(body *sitter.Node, scopes []string) {
	class := strings.Join(scopes, "::")
	from := len(r.out)
	defer func() { r.out = lastOfEachName(r.out, from) }()
	private := false
	for i := uint(0); i < body.NamedChildCount(); i++ {
		stmt := body.NamedChild(i)
		switch kindOf(stmt) {
		case "identifier":
			switch rubyText(stmt, r.src) {
			case "private", "protected":
				private = true
			case "public":
				private = false
			}
		case "method":
			name := rubyText(stmt.ChildByFieldName("name"), r.src)
			if !private && strings.HasPrefix(name, "test_") {
				r.emit(stmt, scopes, name, class+"#"+name)
			}
		case "call":
			method := stmt.ChildByFieldName("method")
			if method == nil || stmt.ChildByFieldName("receiver") != nil || rubyText(method, r.src) != "test" || stmt.ChildByFieldName("block") == nil {
				continue
			}
			args := descriptionArgs(stmt)
			if len(args) != 1 || kindOf(args[0]) != "string" {
				r.unlabelled++
				continue
			}
			label, ok := r.descriptionPart(args[0])
			if !ok {
				r.unlabelled++
				continue
			}
			r.emit(stmt, scopes, label, class+"#test_"+whitespaceRun.ReplaceAllString(label, "_"))
		}
	}
}

func (r *rubyTestReader) emit(node *sitter.Node, groups []string, label, runner string) {
	endLine, col, endCol := tsutil.Span(node, r.src)
	r.out = append(r.out, facts.NewTestCase("ruby", r.relFile, groups, label, runner,
		int(node.StartPosition().Row)+1, endLine, col, endCol))
}

// descriptionArgs are a DSL call's positional arguments, less the trailing
// metadata hash (type: :model) and a block argument.
func descriptionArgs(call *sitter.Node) []*sitter.Node {
	list := call.ChildByFieldName("arguments")
	if list == nil {
		return nil
	}
	var out []*sitter.Node
	for i := uint(0); i < list.NamedChildCount(); i++ {
		arg := list.NamedChild(i)
		switch kindOf(arg) {
		case "pair", "hash", "block_argument", "simple_symbol_pair":
			continue
		case "simple_symbol":
			if len(out) > 0 {
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

// descriptionPart reads one literal description argument, and reports false for
// anything built at run time: interpolation, a method call, a variable.
func (r *rubyTestReader) descriptionPart(arg *sitter.Node) (string, bool) {
	switch kindOf(arg) {
	case "string":
		// An escape is decoded by Ruby at run time, and reading one by hand is a
		// guess at what it becomes, so a label holding one is not a literal here.
		for i := uint(0); i < arg.NamedChildCount(); i++ {
			if kindOf(arg.NamedChild(i)) != "string_content" {
				return "", false
			}
		}
		return stringLiteralContent(arg, r.src), true
	case "simple_symbol":
		return strings.TrimPrefix(rubyText(arg, r.src), ":"), true
	case "constant", "scope_resolution":
		return stripLeadingColons(rubyText(arg, r.src)), true
	}
	return "", false
}

func (r *rubyTestReader) literal(arg *sitter.Node) string {
	s, _ := r.descriptionPart(arg)
	return s
}

func isConstantNode(n *sitter.Node) bool {
	k := kindOf(n)
	return k == "constant" || k == "scope_resolution"
}

func startsLikeAMember(s string) bool {
	return strings.HasPrefix(s, "#") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "::")
}

// lastOfEachName keeps, among the cases read from one class body, the last with
// each runner name: a method defined again replaces the first, and minitest runs
// what the class holds.
func lastOfEachName(cases []facts.Fact, from int) []facts.Fact {
	last := map[string]int{}
	for i := from; i < len(cases); i++ {
		last[cases[i].PropString("runner_name")] = i
	}
	kept := cases[:from]
	for i := from; i < len(cases); i++ {
		if last[cases[i].PropString("runner_name")] == i {
			kept = append(kept, cases[i])
		}
	}
	return kept
}
