package scalaextractor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanScala: definitions span their node.
func TestSymbolSpanScala(t *testing.T) {
	src := "package example\n\nclass Rect {\n  def area: Int = {\n    1\n  }\n}\n"
	got := map[string][4]int{}
	for _, f := range extractFileAST([]byte(src), "src/main/scala/example/Rect.scala", nil) {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{"src/main/scala/example.Rect": {3, 7, 1, 2}, "src/main/scala/example.Rect.area": {4, 6, 3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestTestCaseFactsScala: MUnit and FunSuite tests by their label, FunSpec tests
// by their describe texts and their own joined with spaces.
func TestTestCaseFactsScala(t *testing.T) {
	src := `class S extends munit.FunSuite {
  test("plain") {
  }
  describe("outer") {
    describe("inner") {
      it("deep") {
      }
    }
  }
}
`
	reading := scalaTestCases([]byte(src), "src/test/scala/S.scala")
	var got []string
	for _, f := range reading.cases {
		groups, _ := f.Props["groups"].([]string)
		got = append(got, f.Props["runner_name"].(string)+"|"+strings.Join(groups, "/"))
		if len(f.Relations) != 0 {
			t.Errorf("relations on %s", f.Name)
		}
	}
	want := []string{"plain|", "outer inner deep|outer/inner"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
}

// TestTestCaseLabelNotLiteral_Scala: an interpolation, an escape, a label
// expression and every test under a describe whose label is not a literal are
// counted, not named.
func TestTestCaseLabelNotLiteral_Scala(t *testing.T) {
	src := `class S extends munit.FunSuite {
  test(s"side $n") {
  }
  test("tab\tname") {
  }
  test("tagged".tag(Slow)) {
  }
  describe(name) {
    it("a") {
    }
  }
}
`
	reading := scalaTestCases([]byte(src), "src/test/scala/S.scala")
	if reading.unlabelled != 4 || len(reading.cases) != 0 {
		t.Errorf("unlabelled %d, cases %d; want 4 and 0", reading.unlabelled, len(reading.cases))
	}
}
