package swiftextractor

import (
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// inheritsXCTestCase reads a supertype named XCTestCase or any *TestCase, the
// name suites give the base class they put between XCTestCase and their tests
// (Alamofire's BaseTestCase), which is the same rule the Java extractor applies.
func inheritsXCTestCase(supertypes []string) bool {
	for _, s := range supertypes {
		if strings.HasSuffix(s, "TestCase") {
			return true
		}
	}
	return false
}

// xctestSignature is XCTest's rule for a test method: an instance method named
// test* that takes no arguments.
var xctestSignature = regexp.MustCompile(`func\s+test\w*\s*\(\s*\)`)

// swiftTestStyle says whether a function is a test and which runner names it:
// XCTest runs a test*() method of an XCTestCase subclass, and swift-testing runs a
// function marked @Test, in a type or at the top level.
func (w *astWalker) swiftTestStyle(name string, attrs []string, header string) string {
	for _, a := range attrs {
		if a == "Test" {
			return "swift-testing"
		}
	}
	if len(w.xctestStack) > 0 && w.xctestStack[len(w.xctestStack)-1] && strings.HasPrefix(name, "test") &&
		xctestSignature.MatchString(header) && !strings.Contains(header, "static ") && !strings.Contains(header, "class func") {
		return "xctest"
	}
	return ""
}

// emitSwiftTest names a test the way `swift test list` prints it: the test target,
// the type and the function, Target.Type/testMethod for XCTest and
// Target.Type/function() for swift-testing. The target is the directory under
// Tests/ that SwiftPM builds it from; outside that layout it is left out.
func (w *astWalker) emitSwiftTest(node *sitter.Node, name, style string) {
	function := name
	if style == "swift-testing" {
		function += "()"
	}
	runner := function
	if len(w.typeStack) > 0 {
		runner = strings.Join(w.typeStack, ".") + "/" + function
	}
	if target := swiftTestTarget(w.relFile); target != "" {
		runner = target + "." + runner
	}
	endLine, col, endCol := tsutil.Span(node, w.src)
	tc := facts.NewTestCase("swift", w.relFile, append([]string(nil), w.typeStack...), name, runner,
		int(node.StartPosition().Row)+1, endLine, col, endCol)
	tc.SetProp("style", style)
	w.testCases = append(w.testCases, tc)
}

func swiftTestTarget(relFile string) string {
	segments := strings.Split(relFile, "/")
	for i := 0; i+2 < len(segments); i++ {
		if segments[i] == "Tests" {
			return segments[i+1]
		}
	}
	return ""
}
