package dartextractor

import (
	"reflect"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanDart: a function runs from its signature to its body, which the
// grammar makes the signature's sibling.
func TestSymbolSpanDart(t *testing.T) {
	src := "class Rect {\n  int area() {\n    return 1;\n  }\n}\n\nint twice(int x) {\n  return 2 * x;\n}\n"
	res := extractFile([]byte(src), "lib/rect.dart", &packageIndex{byName: map[string]*pubPackage{}}, nil)
	got := map[string][4]int{}
	for _, f := range res.facts {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{"lib.Rect": {1, 5, 1, 2}, "lib.Rect.area": {2, 4, 3, 4}, "lib.twice": {7, 9, 1, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

func parseDart(t *testing.T, src string) (*sitter.Tree, []byte) {
	t.Helper()
	pp, _ := parserPool.Get().(*sitter.Parser)
	defer parserPool.Put(pp)
	b := []byte(src)
	return pp.Parse(b, nil), b
}

// TestTestCaseFactsDart: tests by their group descriptions and their own joined
// by spaces.
func TestTestCaseFactsDart(t *testing.T) {
	tree, src := parseDart(t, "void main() {\n  test('top', () {});\n  group('g', () {\n    testWidgets('w', (t) async {});\n  });\n}\n")
	defer tree.Close()
	reading := dartTestCases(tree.RootNode(), src, "test/a_test.dart")
	var got []string
	for _, f := range reading.cases {
		groups, _ := f.Props["groups"].([]string)
		got = append(got, f.Props["runner_name"].(string)+"|"+strings.Join(groups, "/"))
	}
	if !reflect.DeepEqual(got, []string{"top|", "g w|g"}) {
		t.Errorf("tests = %v", got)
	}
}

// TestTestCaseLabelNotLiteral_Dart: a substitution, an escape and every test under
// a non-literal group are counted, not named.
func TestTestCaseLabelNotLiteral_Dart(t *testing.T) {
	tree, src := parseDart(t, "void main() {\n  test('a $x', () {});\n  test('it\\'s', () {});\n  group(name, () {\n    test('in', () {});\n  });\n}\n")
	defer tree.Close()
	reading := dartTestCases(tree.RootNode(), src, "test/a_test.dart")
	if reading.unlabelled != 3 || len(reading.cases) != 0 {
		t.Errorf("unlabelled %d, cases %d; want 3 and 0", reading.unlabelled, len(reading.cases))
	}
}
