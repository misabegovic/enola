package pythonextractor

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const pySpanSource = `import math


def area(w, h):
    """The area."""
    return w * h


@traced
def perimeter(w, h):
    return 2 * (w + h)


class Rect:
    def scale(self, k):
        self.w *= k
        return self
`

// TestSymbolSpanPython: every symbol spans its definition, from def or class to
// the last line of its body as CPython's ast reports it (end_lineno and
// end_col_offset), with a decorator outside the span.
func TestSymbolSpanPython(t *testing.T) {
	ff, _ := extractFileAST([]byte(pySpanSource), "pkg/shapes.py", false, false, false, nil)
	type span struct{ line, end, col, endCol int }
	want := map[string]span{
		"pkg/shapes.area":       {4, 6, 1, 17},
		"pkg/shapes.perimeter":  {10, 11, 1, 23},
		"pkg/shapes.Rect":       {14, 17, 1, 20},
		"pkg/shapes.Rect.scale": {15, 17, 5, 20},
		"pkg/shapes -> math":    {1, 1, 1, 12},
	}
	got := map[string]span{}
	for _, f := range ff {
		if _, ok := want[f.Name]; ok {
			got[f.Name] = span{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans:\n got %v\nwant %v", got, want)
	}
}

const pyTestSource = `import unittest
import pytest


def test_area():
    assert True


@pytest.mark.parametrize("n", [1, 2])
def test_rows(n):
    assert n


def helper():
    pass


class TestRect:
    def test_scale(self):
        assert True

    class TestNested:
        def test_deep(self):
            assert True


class TestWithInit:
    def __init__(self):
        pass

    def test_never(self):
        pass


class Plain:
    def test_not_collected(self):
        pass


class Skipped:
    __test__ = False


class Case(unittest.TestCase):
    def test_unit(self):
        self.assertTrue(True)

    def setUp(self):
        pass
`

// TestTestCaseFactsPython: the tests pytest collects by its default rules, each
// named by its node id without the file, with its groups, its span and no
// relation; a parametrised row, a class with __init__, a class pytest does not
// collect and a non-test module give none.
func TestTestCaseFactsPython(t *testing.T) {
	file := "tests/test_shapes.py"
	type row struct {
		runner, groups string
		line, end      int
	}
	var got []row
	for _, f := range pyTestCases([]byte(pyTestSource), file) {
		if f.Kind != facts.KindTestCase || len(f.Relations) != 0 || f.File != file {
			t.Errorf("not a bare test_case: %+v", f)
		}
		runner, _ := f.Props["runner_name"].(string)
		if f.Name != file+"::"+runner || f.Props["language"] != "python" {
			t.Errorf("name %q does not carry runner %q", f.Name, runner)
		}
		groups, _ := f.Props["groups"].([]string)
		got = append(got, row{runner, strings.Join(groups, "/"), f.Line, f.EndLine})
	}
	want := []row{
		{"test_area", "", 5, 6},
		{"test_rows", "", 10, 11},
		{"TestRect::test_scale", "TestRect", 19, 20},
		{"TestRect::TestNested::test_deep", "TestRect/TestNested", 23, 24},
		{"Case::test_unit", "Case", 45, 46},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("test cases:\n got %+v\nwant %+v", got, want)
	}
	if extra := pyTestCases([]byte(pyTestSource), "tests/helpers.py"); len(extra) != 0 {
		t.Errorf("a module pytest does not collect gave %d test cases", len(extra))
	}
	if extra := pyTestCases([]byte(pyTestSource), "tests/conftest.py"); len(extra) != 0 {
		t.Errorf("conftest.py gave %d test cases", len(extra))
	}
}

// The reference-only pass keeps its test cases even though they carry no edges,
// which is what drops a test_ref with nothing resolved.
func TestTestCaseFactsPython_ThroughTheTestPass(t *testing.T) {
	repo := t.TempDir()
	writePy(t, repo, "tests/test_only.py", "def test_nothing():\n    assert True\n")
	ff, err := New().ExtractTestRefs(context.Background(), repo, []string{"tests/test_only.py"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ff) != 1 || ff[0].Kind != facts.KindTestCase || ff[0].Name != "tests/test_only.py::test_nothing" {
		t.Errorf("got %+v", ff)
	}
}

// TestTestCaseShadowedPython: a test defined twice under one name is collected
// once, from the later definition, which is what the module holds; a class
// rebound by a subclass of the same name passes its tests on, and one the
// subclass redefines is its own.
func TestTestCaseShadowedPython(t *testing.T) {
	src := "def test_twice():\n    assert False\n\n\ndef test_twice():\n    assert True\n\n\nclass TestBox:\n    def test_a(self):\n        pass\n\n    def test_a(self):\n        pass\n\n\nclass TestBound(SimpleTestCase):\n    def test_kept(self):\n        pass\n\n    def test_redone(self):\n        pass\n\n\nclass TestBound(TestBound):\n    def test_redone(self):\n        pass\n\n\nclass TestGone:\n    def test_lost(self):\n        pass\n\n\nclass TestGone:\n    pass\n"
	var got []string
	for _, f := range pyTestCases([]byte(src), "tests/test_dup.py") {
		got = append(got, f.Props["runner_name"].(string)+"@"+strconv.Itoa(f.Line))
	}
	want := []string{"test_twice@5", "TestBox::test_a@13", "TestBound::test_kept@18", "TestBound::test_redone@26"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
}
