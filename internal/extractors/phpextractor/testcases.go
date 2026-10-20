package phpextractor

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

func extendsTestCase(parents []string) bool {
	for _, p := range parents {
		if strings.HasSuffix(p, "TestCase") {
			return true
		}
	}
	return false
}

// phpunitTest is PHPUnit's rule for a method of a TestCase subclass: a public
// method whose name starts with test, or one carrying the #[Test] attribute or a
// @test docblock tag.
func (w *phpWalker) phpunitTest(method *sitter.Node, name string) bool {
	if len(w.testCaseStack) == 0 || !w.testCaseStack[len(w.testCaseStack)-1] {
		return false
	}
	if methodVisibility(method, w.src) != "public" {
		return false
	}
	if strings.HasPrefix(name, "test") {
		return true
	}
	for i := uint(0); i < method.NamedChildCount(); i++ {
		c := method.NamedChild(i)
		if kindOf(c) != "attribute_list" {
			continue
		}
		for _, word := range strings.FieldsFunc(phpText(c, w.src), func(r rune) bool {
			return !(r == '\\' || r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
		}) {
			if word == "Test" || strings.HasSuffix(word, `\Test`) {
				return true
			}
		}
	}
	if prev := method.PrevNamedSibling(); prev != nil && kindOf(prev) == "comment" {
		for _, tag := range strings.Fields(phpText(prev, w.src)) {
			if tag == "@test" {
				return true
			}
		}
	}
	return false
}

// pestCases names Pest's top-level tests: test('label', ...) by its label and
// it('label', ...) by "it " and its label, which is the description Pest gives
// it. A test inside describe() takes a name built from the describe as well and is
// not named here.
func pestCases(root *sitter.Node, src []byte, relFile string) ([]facts.Fact, int) {
	var out []facts.Fact
	unlabelled := 0
	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if kindOf(stmt) != "expression_statement" || stmt.NamedChildCount() == 0 {
			continue
		}
		call := stmt.NamedChild(0)
		for kindOf(call) == "member_call_expression" {
			// test('x', fn)->with([...]) and ->skip() chain on the registration.
			call = call.ChildByFieldName("object")
		}
		if kindOf(call) != "function_call_expression" {
			continue
		}
		word := phpText(call.ChildByFieldName("function"), src)
		if word != "test" && word != "it" {
			continue
		}
		args := call.ChildByFieldName("arguments")
		if args == nil || args.NamedChildCount() < 2 {
			continue
		}
		label, ok := phpStringLiteral(args.NamedChild(0), src)
		if !ok {
			unlabelled++
			continue
		}
		runner := label
		if word == "it" {
			runner = "it " + label
		}
		endLine, col, endCol := tsutil.Span(stmt, src)
		out = append(out, facts.NewTestCase("php", relFile, nil, label, runner,
			int(stmt.StartPosition().Row)+1, endLine, col, endCol))
	}
	return out, unlabelled
}

// phpStringLiteral is an argument's text when it is a single-quoted string with no
// escape, or a double-quoted one with no escape or variable.
func phpStringLiteral(arg *sitter.Node, src []byte) (string, bool) {
	if arg == nil {
		return "", false
	}
	if kindOf(arg) == "argument" && arg.NamedChildCount() > 0 {
		arg = arg.NamedChild(0)
	}
	text := phpText(arg, src)
	if len(text) < 2 || strings.Contains(text, `\`) {
		return "", false
	}
	switch {
	case kindOf(arg) == "string" && text[0] == '\'' && text[len(text)-1] == '\'':
		return text[1 : len(text)-1], true
	case kindOf(arg) == "encapsed_string" && text[0] == '"' && text[len(text)-1] == '"' && !strings.ContainsAny(text, "${"):
		return text[1 : len(text)-1], true
	}
	return "", false
}
