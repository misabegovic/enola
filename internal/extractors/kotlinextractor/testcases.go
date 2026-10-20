package kotlinextractor

import (
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// kotlinJUnitTest is JUnit 4 and 5's rule on a Kotlin function: an annotation that
// makes it a test method. A parameterised or repeated test is one function.
func kotlinJUnitTest(annotations []string) bool {
	for _, a := range annotations {
		switch a {
		case "Test", "ParameterizedTest", "RepeatedTest", "TestFactory", "TestTemplate":
			return true
		}
	}
	return false
}

type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
	// nested are tests under a context, whose full name this reader does not
	// compose.
	nested int
}

// kotestCases names the root tests of the Kotest specs whose tests are named by
// their label alone: a FunSpec's test("label") and a StringSpec's "label" { },
// declared in the spec's constructor lambda. The spec class is the group, the
// label the name Kotest's test filter matches. A nested context and every other
// spec style build longer names this reader does not compose, and an
// interpolated label is built at run time; neither is named.
func kotestCases(root *sitter.Node, src []byte, relFile string) testCaseReading {
	var reading testCaseReading
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if kindOf(n) == "class_declaration" {
			if style, body := kotestSpec(n, src); body != nil {
				name := nodeText(n.ChildByFieldName("name"), src)
				for i := uint(0); i < body.NamedChildCount(); i++ {
					readKotestStatement(body.NamedChild(i), src, relFile, name, style, &reading)
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return reading
}

// kotestSpec is the style a class extends and the lambda it hands its constructor.
func kotestSpec(class *sitter.Node, src []byte) (string, *sitter.Node) {
	specs := findChildByKind(class, "delegation_specifiers")
	if specs == nil {
		return "", nil
	}
	for i := uint(0); i < specs.NamedChildCount(); i++ {
		inv := findChildByKind(specs.NamedChild(i), "constructor_invocation")
		if inv == nil {
			continue
		}
		style := nodeText(findChildByKind(inv, "user_type"), src)
		if style != "FunSpec" && style != "StringSpec" {
			continue
		}
		args := findChildByKind(inv, "value_arguments")
		if args == nil {
			continue
		}
		for j := uint(0); j < args.NamedChildCount(); j++ {
			if lambda := findChildByKind(args.NamedChild(j), "lambda_literal"); lambda != nil {
				return style, lambda
			}
		}
	}
	return "", nil
}

func readKotestStatement(stmt *sitter.Node, src []byte, relFile, spec, style string, reading *testCaseReading) {
	if kindOf(stmt) != "call_expression" || findChildByKind(stmt, "annotated_lambda") == nil {
		return
	}
	callee := stmt.NamedChild(0)
	var label *sitter.Node
	switch {
	case style == "StringSpec" && kindOf(callee) == "string_literal":
		label = callee
	case style == "FunSpec" && kindOf(callee) == "call_expression":
		word := nodeText(callee.NamedChild(0), src)
		args := findChildByKind(callee, "value_arguments")
		if args == nil || args.NamedChildCount() == 0 {
			return
		}
		first := args.NamedChild(0).NamedChild(0)
		switch word {
		case "test", "xtest":
			label = first
		case "context", "xcontext":
			reading.nested += countKotestTests(stmt, src)
			return
		default:
			return
		}
	default:
		return
	}
	text, ok := kotlinLiteral(label, src)
	if !ok {
		reading.unlabelled++
		return
	}
	endLine, col, endCol := tsutil.Span(stmt, src)
	reading.cases = append(reading.cases, facts.NewTestCase("kotlin", relFile, []string{spec}, text, text,
		int(stmt.StartPosition().Row)+1, endLine, col, endCol))
}

func countKotestTests(n *sitter.Node, src []byte) int {
	count := 0
	if kindOf(n) == "call_expression" && kindOf(n.NamedChild(0)) == "call_expression" {
		switch nodeText(n.NamedChild(0).NamedChild(0), src) {
		case "test", "xtest":
			count++
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		count += countKotestTests(n.NamedChild(i), src)
	}
	return count
}

// kotlinLiteral is a string literal's text when it holds nothing but text.
func kotlinLiteral(n *sitter.Node, src []byte) (string, bool) {
	if n == nil || kindOf(n) != "string_literal" {
		return "", false
	}
	var b strings.Builder
	for i := uint(0); i < n.NamedChildCount(); i++ {
		part := n.NamedChild(i)
		if kindOf(part) != "string_content" {
			return "", false
		}
		b.WriteString(nodeText(part, src))
	}
	// The grammar leaves a $name template inside the content; Kotlin fills it at run
	// time, so a label holding one is not a literal.
	if kotlinTemplate.MatchString(b.String()) {
		return "", false
	}
	return b.String(), true
}

var kotlinTemplate = regexp.MustCompile(`\$[A-Za-z_{]`)
