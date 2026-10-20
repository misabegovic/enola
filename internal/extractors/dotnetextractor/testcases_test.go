package dotnetextractor

import (
	"reflect"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanCSharp: a class and its method span their declaration.
func TestSymbolSpanCSharp(t *testing.T) {
	src := "namespace Shapes\n{\n    public class Rect\n    {\n        public int Area()\n        {\n            return 1;\n        }\n    }\n}\n"
	got := map[string][4]int{}
	for _, f := range extractFileAST([]byte(src), "src/Rect.cs") {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{"src.Rect": {3, 9, 5, 6}, "src.Rect.Area": {5, 8, 9, 10}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestTestCaseFactsCSharp: xUnit, NUnit and MSTest methods by their fully
// qualified name, a nested class joined with +; a fixture and a helper are not
// tests.
func TestTestCaseFactsCSharp(t *testing.T) {
	src := `namespace Shapes.Tests;

public class RectTests
{
    [Fact]
    public void Area() { }

    [Xunit.Theory, InlineData(1)]
    public void Rows(int n) { }

    [SetUp]
    public void Init() { }

    public class Nested
    {
        [TestMethodAttribute]
        public void Deep() { }
    }
}
`
	var got []string
	for _, f := range csTestCases([]byte(src), "tests/RectTests.cs") {
		if len(f.Relations) != 0 || f.Props["language"] != "csharp" {
			t.Errorf("not a bare csharp test_case: %+v", f)
		}
		got = append(got, f.Props["runner_name"].(string))
	}
	want := []string{"Shapes.Tests.RectTests.Area", "Shapes.Tests.RectTests.Rows", "Shapes.Tests.RectTests+Nested.Deep"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
}

// A test project indexed as production names its tests beside its symbols.
func TestTestCaseFactsCSharp_IndexedAsProduction(t *testing.T) {
	src := "namespace Google.Protobuf\n{\n    public class CodedTest\n    {\n        [Test]\n        public void Reads()\n        {\n        }\n    }\n}\n"
	var tests, symbols []string
	for _, f := range extractFileAST([]byte(src), "csharp/src/Google.Protobuf.Test/CodedTest.cs") {
		switch f.Kind {
		case facts.KindTestCase:
			tests = append(tests, f.Props["runner_name"].(string))
		case facts.KindSymbol:
			symbols = append(symbols, f.Name)
		}
	}
	if !reflect.DeepEqual(tests, []string{"Google.Protobuf.CodedTest.Reads"}) || len(symbols) != 2 {
		t.Errorf("tests %v, symbols %v", tests, symbols)
	}
}
