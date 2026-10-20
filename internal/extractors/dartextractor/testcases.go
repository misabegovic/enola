package dartextractor

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// dartTests and dartGroups are package:test's and flutter_test's registrations:
// test and testWidgets, and group.
var dartTests = map[string]bool{"test": true, "testWidgets": true}
var dartGroups = map[string]bool{"group": true}

type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
}

// dartTestCases names the tests a Dart test file registers, each by the full name
// package:test prints and filters on with --plain-name: the group descriptions
// and its own, joined by spaces. The span is the registration statement. A
// description with a $ substitution or an escape gets no fact and is counted, and
// so is every test under a group whose description is not a literal.
func dartTestCases(root *sitter.Node, src []byte, relFile string) testCaseReading {
	var reading testCaseReading
	var walk func(n *sitter.Node, groups []string)
	walk = func(n *sitter.Node, groups []string) {
		for _, c := range namedChildren(n) {
			word, label, body, ok := dartRegistration(c, src)
			if !ok {
				walk(c, groups)
				continue
			}
			text, literal := dartLiteral(label, src)
			switch {
			case dartGroups[word] && literal:
				walk(body, append(append([]string(nil), groups...), text))
			case dartGroups[word]:
				reading.unlabelled += countDartTests(body, src)
			case !literal:
				reading.unlabelled++
			default:
				endLine, col, endCol := tsutil.Span(c, src)
				reading.cases = append(reading.cases, facts.NewTestCase("dart", relFile, groups, text,
					strings.Join(append(append([]string(nil), groups...), text), " "),
					int(c.StartPosition().Row)+1, endLine, col, endCol))
			}
		}
	}
	walk(root, nil)
	return reading
}

// dartRegistration reads `word('label', () { ... });`, which the grammar gives
// as an expression statement of an identifier and a selector holding the
// arguments.
func dartRegistration(stmt *sitter.Node, src []byte) (string, *sitter.Node, *sitter.Node, bool) {
	if kindOf(stmt) != "expression_statement" {
		return "", nil, nil, false
	}
	kids := namedChildren(stmt)
	if len(kids) < 2 || kindOf(kids[0]) != "identifier" || kindOf(kids[1]) != "selector" {
		return "", nil, nil, false
	}
	word := kids[0].Utf8Text(src)
	if !dartTests[word] && !dartGroups[word] {
		return "", nil, nil, false
	}
	part := childOfKind(kids[1], "argument_part")
	if part == nil {
		return "", nil, nil, false
	}
	args := childOfKind(part, "arguments")
	if args == nil {
		return "", nil, nil, false
	}
	argList := namedChildren(args)
	if len(argList) < 2 {
		return "", nil, nil, false
	}
	var label *sitter.Node
	if first := namedChildren(argList[0]); len(first) > 0 {
		label = first[0]
	}
	return word, label, argList[1], true
}

func dartLiteral(n *sitter.Node, src []byte) (string, bool) {
	if n == nil || kindOf(n) != "string_literal" || len(namedChildren(n)) > 0 {
		return "", false
	}
	text := n.Utf8Text(src)
	if len(text) < 2 || strings.ContainsAny(text, `\$`) || strings.HasPrefix(text, "r") {
		return "", false
	}
	quote := text[0]
	if (quote != '\'' && quote != '"') || text[len(text)-1] != quote || strings.HasPrefix(text, `'''`) || strings.HasPrefix(text, `"""`) {
		return "", false
	}
	return text[1 : len(text)-1], true
}

func countDartTests(n *sitter.Node, src []byte) int {
	if n == nil {
		return 0
	}
	count := 0
	for _, c := range namedChildren(n) {
		if word, _, _, ok := dartRegistration(c, src); ok && dartTests[word] {
			count++
		}
		count += countDartTests(c, src)
	}
	return count
}
