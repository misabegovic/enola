package tsextractor

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanTypeScript: functions, arrow-function constants, classes and their
// members span their declaration, lines as the TypeScript compiler reports them;
// an exported declaration starts at its keyword, export being a modifier.
func TestSymbolSpanTypeScript(t *testing.T) {
	src := `import { x } from "./x";

export function area(w: number, h: number): number {
  return w * h;
}

export const perimeter = (w: number): number => {
  return 4 * w;
};

class Rect {
  scale(k: number): number {
    return k;
  }
}
`
	type span struct{ line, end, col, endCol int }
	want := map[string]span{
		"src.area":       {3, 5, 8, 2},
		"src.perimeter":  {7, 9, 8, 3},
		"src.Rect":       {11, 15, 1, 2},
		"src.Rect.scale": {12, 14, 3, 4},
	}
	got := map[string]span{}
	for _, f := range extractTS(t, src, "src/shapes.ts") {
		if _, ok := want[f.Name]; ok && f.Kind == facts.KindSymbol {
			got[f.Name] = span{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
		if f.Kind == facts.KindDependency && (f.EndLine != 1 || f.Column != 1) {
			t.Errorf("import span = %d-%d col %d", f.Line, f.EndLine, f.Column)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans:\n got %v\nwant %v", got, want)
	}
}

const tsTestSource = `import { describe, it, test } from "vitest";

describe("area", () => {
  it("multiplies", () => {});

  describe("of a square", () => {
    test.only("is squared", () => {});
  });

  it.each([1, 2])("row %i", () => {});
  it(` + "`" + `interpolates ${1}` + "`" + `, () => {});
});

describe.each([1])("table %i", () => {
  it("inside a table", () => {});
});

test("top level", () => {});
it.todo("later");

test.describe("playwright group", () => {
  test("inside", async () => {});
});

test.extend({}).describe("custom", () => {
  test("hidden", () => {});
});
`

// TestTestCaseFactsTypeScript: tests named by jest's full name, the group labels
// and their own joined by spaces, with the call's span and no relation.
func TestTestCaseFactsTypeScript(t *testing.T) {
	type row struct {
		runner, groups string
		line, end      int
	}
	var got []row
	for _, f := range jsTestFileCases([]byte(tsTestSource), "src/area.test.ts").cases {
		if len(f.Relations) != 0 || f.Kind != facts.KindTestCase || f.File != "src/area.test.ts" {
			t.Errorf("not a bare test_case: %+v", f)
		}
		groups, _ := f.Props["groups"].([]string)
		got = append(got, row{f.Props["runner_name"].(string), strings.Join(groups, "/"), f.Line, f.EndLine})
	}
	want := []row{
		{"area multiplies", "area", 4, 4},
		{"area of a square is squared", "area/of a square", 7, 7},
		{"top level", "", 18, 18},
		{"later", "", 19, 19},
		{"playwright group inside", "playwright group", 22, 22},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests:\n got %+v\nwant %+v", got, want)
	}
	for _, name := range []string{"src/area.ts", "src/fixtures/a.test.ts", "tests/unit/area-test.ts", "node_modules/x/a.test.js"} {
		if isJSTestFile(name) {
			t.Errorf("%s read as a describe/it test file", name)
		}
	}
	for _, name := range []string{"src/area.test.ts", "src/area.spec.jsx", "src/__tests__/area.js", "lib/a.test.mjs"} {
		if !isJSTestFile(name) {
			t.Errorf("%s not read as a describe/it test file", name)
		}
	}
}

// TestTestCaseLabelNotLiteral_TypeScript: a table's rows, an interpolated label and
// the tests under a DSL member the reader does not know are counted, not named,
// and each pass reports its count as coverage.
func TestTestCaseLabelNotLiteral_TypeScript(t *testing.T) {
	reading := jsTestFileCases([]byte(tsTestSource), "src/area.test.ts")
	if reading.unlabelled != 4 {
		t.Errorf("unlabelled = %d, want 4 (it.each, the interpolated it, the one inside describe.each, the one under test.extend)", reading.unlabelled)
	}
	dir := setupTSProject(t, map[string]string{"src/area.test.ts": tsTestSource}, false)
	ff, err := New().ExtractTestRefs(context.Background(), dir, []string{"src/area.test.ts"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var coverage *facts.Fact
	for i := range ff {
		if ff[i].Kind == facts.KindExtraction && ff[i].Name == "typescript:test_cases" {
			coverage = &ff[i]
		}
	}
	if coverage == nil || coverage.Props["unresolved_macros"] != "label_not_literal=4" {
		t.Errorf("coverage = %+v", coverage)
	}
}
