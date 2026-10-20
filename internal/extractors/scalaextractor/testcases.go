package scalaextractor

import (
	"strings"

	scala "github.com/enola-labs/enola/internal/extractors/scalaextractor/grammar"
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
}

// scalaTests and scalaGroups are the ScalaTest and MUnit calls that register a
// test or open a group with a literal label: FunSuite's and MUnit's
// test("label") { }, and FunSpec's describe("label") { it("label") { } }.
var scalaTests = map[string]bool{"test": true, "it": true, "ignore": true}
var scalaGroups = map[string]bool{"describe": true}

// scalaTestCases names the tests a Scala test file registers, each by ScalaTest's
// full test name: the describe labels and its own, joined by spaces, which for a
// FunSuite or MUnit test is its label alone. A label that is not a plain string
// literal (an s"" interpolation, an escape) gets no fact and is counted.
func scalaTestCases(src []byte, relFile string) testCaseReading {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(scala.Language())); err != nil {
		return testCaseReading{}
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return testCaseReading{}
	}
	defer tree.Close()
	var reading testCaseReading
	var walk func(n *sitter.Node, groups []string)
	walk = func(n *sitter.Node, groups []string) {
		if word, arg, block := scalaRegistration(n, src); word != "" {
			label, ok := scalaLiteral(arg, src)
			switch {
			case scalaGroups[word] && ok:
				next := append(append([]string(nil), groups...), label)
				walk(block, next)
				return
			case scalaGroups[word]:
				reading.unlabelled += countScalaTests(block, src)
				return
			case !ok:
				reading.unlabelled++
				return
			default:
				endLine, col, endCol := tsutil.Span(n, src)
				reading.cases = append(reading.cases, facts.NewTestCase("scala", relFile, groups, label,
					strings.Join(append(append([]string(nil), groups...), label), " "),
					int(n.StartPosition().Row)+1, endLine, col, endCol))
				return
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), groups)
		}
	}
	walk(tree.RootNode(), nil)
	return reading
}

// scalaRegistration reads word(arg) { block }, the shape every registration here
// takes: the grammar gives it as a call whose function is itself the call.
func scalaRegistration(n *sitter.Node, src []byte) (string, *sitter.Node, *sitter.Node) {
	if kindOf(n) != "call_expression" {
		return "", nil, nil
	}
	inner := n.ChildByFieldName("function")
	block := n.ChildByFieldName("arguments")
	if inner == nil || block == nil || kindOf(inner) != "call_expression" || kindOf(block) != "block" {
		return "", nil, nil
	}
	fn := inner.ChildByFieldName("function")
	args := inner.ChildByFieldName("arguments")
	if fn == nil || args == nil || kindOf(fn) != "identifier" || args.NamedChildCount() == 0 {
		return "", nil, nil
	}
	word := scalaNodeText(fn, src)
	if !scalaTests[word] && !scalaGroups[word] {
		return "", nil, nil
	}
	return word, args.NamedChild(0), block
}

func scalaLiteral(n *sitter.Node, src []byte) (string, bool) {
	if n == nil || kindOf(n) != "string" {
		return "", false
	}
	text := scalaNodeText(n, src)
	if strings.HasPrefix(text, `"""`) || !strings.HasPrefix(text, `"`) || !strings.HasSuffix(text, `"`) || len(text) < 2 ||
		strings.Contains(text, `\`) {
		return "", false
	}
	return text[1 : len(text)-1], true
}

func countScalaTests(n *sitter.Node, src []byte) int {
	if n == nil {
		return 0
	}
	count := 0
	if word, _, _ := scalaRegistration(n, src); scalaTests[word] {
		count++
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		count += countScalaTests(n.NamedChild(i), src)
	}
	return count
}

func scalaNodeText(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return string(src[n.StartByte():n.EndByte()])
}
