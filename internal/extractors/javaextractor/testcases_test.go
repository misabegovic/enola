package javaextractor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const javaTestSource = `package com.example;

import org.junit.jupiter.api.Test;

class RectTest {
    @Test
    void areaMultiplies() {
        int x = 1;
    }

    @org.junit.jupiter.params.ParameterizedTest
    @ValueSource(ints = {1, 2})
    void rows(int n) {
    }

    @BeforeEach
    void setUp() {
    }

    void helper() {
    }

    @Nested
    class Inner {
        @Test
        void deep() {
        }
    }
}
`

// TestSymbolSpanJava: a class, a method and an import span their declaration,
// an annotated method from its first annotation to its closing brace.
func TestSymbolSpanJava(t *testing.T) {
	ff := extractFileAST([]byte(javaTestSource), "src/test/java/com/example/RectTest.java")
	type span struct{ line, end, col, endCol int }
	dir := "src/test/java/com/example"
	want := map[string]span{
		dir + ".RectTest":                {5, 29, 1, 2},
		dir + ".RectTest.areaMultiplies": {6, 9, 5, 6},
		dir + ".RectTest.setUp":          {16, 18, 5, 6},
		dir + ".RectTest.Inner.deep":     {25, 27, 9, 10},
	}
	got := map[string]span{}
	for _, f := range ff {
		if _, ok := want[f.Name]; ok && f.Kind == facts.KindSymbol {
			got[f.Name] = span{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
		if f.Kind == facts.KindDependency && (f.Line != 3 || f.EndLine != 3 || f.Column != 1 || f.EndColumn != 35) {
			t.Errorf("import span = %+v", f)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans:\n got %v\nwant %v", got, want)
	}
}

// TestTestCaseFactsJava: a method JUnit runs is a test_case beside its symbol,
// named by package, classes and method; a fixture or a helper is not.
func TestTestCaseFactsJava(t *testing.T) {
	type row struct {
		runner, groups string
		line, end      int
	}
	var got []row
	for _, f := range extractFileAST([]byte(javaTestSource), "src/test/java/com/example/RectTest.java") {
		if f.Kind != facts.KindTestCase {
			continue
		}
		if len(f.Relations) != 0 || f.Props["language"] != "java" {
			t.Errorf("not a bare java test_case: %+v", f)
		}
		groups, _ := f.Props["groups"].([]string)
		got = append(got, row{f.Props["runner_name"].(string), strings.Join(groups, "/"), f.Line, f.EndLine})
	}
	want := []row{
		{"com.example.RectTest.areaMultiplies", "RectTest", 6, 9},
		{"com.example.RectTest.rows", "RectTest", 11, 14},
		{"com.example.RectTest.Inner.deep", "RectTest/Inner", 25, 27},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests:\n got %+v\nwant %+v", got, want)
	}
}

// A public no-argument test* method in a TestCase subclass runs under JUnit 3's
// rule with no annotation; a private one, one with parameters and one outside such
// a class do not.
func TestTestCaseFactsJava_JUnit3(t *testing.T) {
	src := `package org.example;

public class SettingsTests extends ESTestCase {
    public void testParses() {
    }

    public void testWithArgs(int n) {
    }

    private void testHidden() {
    }

    public void helper() {
    }
}

class Plain extends Base {
    public void testNotRun() {
    }
}
`
	var got []string
	for _, f := range extractFileAST([]byte(src), "src/test/java/org/example/SettingsTests.java") {
		if f.Kind == facts.KindTestCase {
			got = append(got, f.Props["runner_name"].(string))
		}
	}
	if !reflect.DeepEqual(got, []string{"org.example.SettingsTests.testParses"}) {
		t.Errorf("JUnit 3 tests = %v", got)
	}
}
