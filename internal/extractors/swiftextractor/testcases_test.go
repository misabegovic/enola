package swiftextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanSwift: a type and its method span their declaration.
func TestSymbolSpanSwift(t *testing.T) {
	src := "struct Rect {\n    func area() -> Int {\n        return 1\n    }\n}\n"
	got := map[string][4]int{}
	for _, f := range extractFileAST([]byte(src), "Sources/Shapes/Rect.swift", false) {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{"Sources/Shapes.Rect": {1, 5, 1, 2}, "Sources/Shapes.Rect.area": {2, 4, 5, 6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestTestCaseFactsSwift: XCTest's test*() methods of an XCTestCase subclass and
// swift-testing's @Test functions, named as swift test list prints them.
func TestTestCaseFactsSwift(t *testing.T) {
	src := `final class RectTests: XCTestCase {
    func testArea() {}
    func testArg(_ n: Int) {}
    func helper() {}
}

class Plain: Base {
    func testNotRun() {}
}

class ViaBase: BaseTestCase {
    func testInherited() {}
}

@Test func freeFunction() {}
`
	var got []string
	for _, f := range extractFileAST([]byte(src), "Tests/ShapesTests/RectTests.swift", false) {
		if f.Kind == facts.KindTestCase {
			got = append(got, f.Props["runner_name"].(string)+"|"+f.Props["style"].(string))
		}
	}
	sort.Strings(got)
	want := []string{"ShapesTests.RectTests/testArea|xctest", "ShapesTests.ViaBase/testInherited|xctest", "ShapesTests.freeFunction()|swift-testing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
}
