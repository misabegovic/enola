package dotnetextractor

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// csTestAttributes are the attributes that make a method a test for xUnit
// ([Fact], [Theory]), NUnit ([Test], [TestCase], [TestCaseSource]) and MSTest
// ([TestMethod], [DataTestMethod]). A theory or a test case with rows is one method
// however many rows it runs.
var csTestAttributes = map[string]bool{
	"Fact": true, "Theory": true,
	"Test": true, "TestCase": true, "TestCaseSource": true,
	"TestMethod": true, "DataTestMethod": true,
}

// csTestCases names the test methods a C# test file declares, each by the fully
// qualified name `dotnet test --filter FullyQualifiedName` matches: the type's full
// name (namespace dotted, nested types joined with +) and the method. The span is the method from its
// first attribute to its closing brace.
func csTestCases(src []byte, relFile string) []facts.Fact {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(csharp.Language())); err != nil {
		return nil
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	var out []facts.Fact
	fileNamespace := ""
	var walk func(n *sitter.Node, ns string, classes []string)
	walk = func(n *sitter.Node, ns string, classes []string) {
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch kindOf(c) {
			case "file_scoped_namespace_declaration":
				fileNamespace = nodeText(c.ChildByFieldName("name"), src)
				walk(c, fileNamespace, classes)
			case "namespace_declaration":
				inner := nodeText(c.ChildByFieldName("name"), src)
				if ns != "" {
					inner = ns + "." + inner
				}
				walk(c, inner, classes)
			case "declaration_list":
				walk(c, ns, classes)
			case "class_declaration", "record_declaration", "struct_declaration":
				name := nodeText(c.ChildByFieldName("name"), src)
				if body := c.ChildByFieldName("body"); body != nil && name != "" {
					walk(body, ns, append(append([]string(nil), classes...), name))
				}
			case "method_declaration":
				if len(classes) == 0 || !csTestMethod(c, src) {
					continue
				}
				name := nodeText(c.ChildByFieldName("name"), src)
				// A nested type's full name joins it to its outer type with +, as
				// reflection and the test platform write it.
				runner := strings.Join(classes, "+") + "." + name
				if ns == "" {
					ns = fileNamespace
				}
				if ns != "" {
					runner = ns + "." + runner
				}
				endLine, col, endCol := tsutil.Span(c, src)
				out = append(out, facts.NewTestCase("csharp", relFile, classes, name, runner,
					int(c.StartPosition().Row)+1, endLine, col, endCol))
			default:
				if ns == "" && fileNamespace != "" {
					walk(c, fileNamespace, classes)
				}
			}
		}
	}
	walk(tree.RootNode(), "", nil)
	return out
}

func csTestMethod(method *sitter.Node, src []byte) bool {
	for _, a := range parseAttributes(method, src) {
		if csTestAttributes[a.Name] {
			return true
		}
	}
	return false
}
