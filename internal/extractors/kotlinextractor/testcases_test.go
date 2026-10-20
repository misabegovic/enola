package kotlinextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const kotlinTestSource = `package com.example

class RectTest {
    @Test
    fun ` + "`area multiplies`" + `() {
        check(true)
    }

    @BeforeEach
    fun setUp() {
    }
}

class AreaSpec : FunSpec({
    test("is positive") {
        check(true)
    }

    test("side $n") {
    }

    context("empty") {
        test("no area") {
        }
    }
})

class NameSpec : StringSpec({
    "a square" {
    }
})
`

// TestSymbolSpanKotlin: classes and functions span their declaration.
func TestSymbolSpanKotlin(t *testing.T) {
	ff := extractFileAST([]byte("package p\n\nclass Rect {\n    fun area(): Int {\n        return 1\n    }\n}\n"), "src/Rect.kt", false, "", "", nil)
	got := map[string][4]int{}
	for _, f := range ff {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{"src.Rect": {3, 7, 1, 2}, "src.Rect.area": {4, 6, 5, 6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestTestCaseFactsKotlin: JUnit functions by package.Class.function, Kotest root
// tests by their label.
func TestTestCaseFactsKotlin(t *testing.T) {
	ff, reading := extractFileCountingTests([]byte(kotlinTestSource), "src/test/kotlin/RectTest.kt", false, "", "", nil)
	var got []string
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			if len(f.Relations) != 0 {
				t.Errorf("relations on %s", f.Name)
			}
			got = append(got, f.Props["runner_name"].(string))
		}
	}
	sort.Strings(got)
	want := []string{"a square", "com.example.RectTest.area multiplies", "is positive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
	if reading.unlabelled != 1 || reading.nested != 1 {
		t.Errorf("unlabelled %d nested %d, want 1 and 1", reading.unlabelled, reading.nested)
	}
}

// TestTestCaseLabelNotLiteral_Kotlin: a $ template in a label is not a literal.
func TestTestCaseLabelNotLiteral_Kotlin(t *testing.T) {
	for label, literal := range map[string]bool{"side $n": false, "side ${n}": false, "costs $5": true, "plain": true} {
		src := "package p\n\nclass S : StringSpec({\n    \"" + label + "\" {\n    }\n})\n"
		_, reading := extractFileCountingTests([]byte(src), "src/test/kotlin/S.kt", false, "", "", nil)
		if (reading.unlabelled == 0) != literal {
			t.Errorf("%q: unlabelled %d, literal %v", label, reading.unlabelled, literal)
		}
	}
}
