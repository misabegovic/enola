package cppextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanCpp: a class, an inline method, a field and a free function each
// run from their first token to one past their last. A .hpp, since a lone .h is
// read as C.
func TestSymbolSpanCpp(t *testing.T) {
	src := "namespace geo {\nclass Rect {\n public:\n  int Area() const {\n    return w_ * h_;\n  }\n  int w_;\n  int h_;\n};\n}\n\nint Twice(int x) {\n  return 2 * x;\n}\n"
	ff := extractProject(t, map[string]string{"src/rect.hpp": src})
	got := map[string][4]int{}
	for _, f := range ff {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{
		"src.geo::Rect":       {2, 9, 1, 2},
		"src.geo::Rect::Area": {4, 6, 3, 4},
		"src.geo::Rect::w_":   {7, 7, 3, 10},
		"src.geo::Rect::h_":   {8, 8, 3, 10},
		"src.Twice":           {12, 14, 1, 2},
	}
	for name, span := range want {
		if got[name] != span {
			t.Errorf("%s span = %v, want %v (all: %v)", name, got[name], span, got)
		}
	}
}

const gtestFile = `#include <gtest/gtest.h>

namespace {

TEST(RectTest, AreaMultiplies) {
  EXPECT_EQ(6, 6);
}

class SquareFixture : public ::testing::Test {};

TEST_F(SquareFixture, SquareOfSide) {
  EXPECT_EQ(4, 4);
}

class SideTest : public ::testing::TestWithParam<int> {};

TEST_P(SideTest, IsNonNegative) {
  EXPECT_GE(GetParam(), 0);
}

}  // namespace
`

const catchFile = `#include <catch2/catch_test_macros.hpp>

TEST_CASE("area multiplies", "[rect]") {
  REQUIRE(6 == 6);
}

TEST_CASE_METHOD(Fixture, "side squares") {
  REQUIRE(4 == 4);
}

TEST_CASE(kName) {
}
`

func testCaseRunners(ff []facts.Fact) []string {
	var out []string
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			out = append(out, f.Props["runner_name"].(string))
		}
	}
	sort.Strings(out)
	return out
}

// TestTestCaseFactsCpp: GoogleTest's fixed tests by Suite.Name, inside an
// anonymous namespace too, and Catch2's by their name, spanning their bodies.
func TestTestCaseFactsCpp(t *testing.T) {
	ff := extractProject(t, map[string]string{"test/rect_test.cc": gtestFile, "test/rect_catch.cc": catchFile})
	if got, want := testCaseRunners(ff), []string{"RectTest.AreaMultiplies", "SquareFixture.SquareOfSide", "area multiplies", "side squares"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runners = %v, want %v", got, want)
	}
	area := mustFact(t, ff, "test/rect_test.cc::RectTest.AreaMultiplies")
	if span := [4]int{area.Line, area.EndLine, area.Column, area.EndColumn}; span != [4]int{5, 7, 1, 2} {
		t.Errorf("gtest span = %v", span)
	}
	catch := mustFact(t, ff, "test/rect_catch.cc::area multiplies")
	if span := [4]int{catch.Line, catch.EndLine, catch.Column, catch.EndColumn}; span != [4]int{3, 5, 1, 2} {
		t.Errorf("catch2 span = %v", span)
	}
	if groups, _ := area.Props["groups"].([]string); !reflect.DeepEqual(groups, []string{"RectTest"}) {
		t.Errorf("gtest groups = %v", groups)
	}
}

// TestTestCaseCoverageCpp: a parameterised test is counted under its own cause,
// a Catch2 name that is not a literal under another, and neither gets a fact.
func TestTestCaseCoverageCpp(t *testing.T) {
	ff := extractProject(t, map[string]string{"test/rect_test.cc": gtestFile, "test/rect_catch.cc": catchFile})
	cov := mustFact(t, ff, "cpp:test_cases")
	edges, _ := cov.Props["edge_coverage"].([]map[string]any)
	if len(edges) != 1 || edges[0]["resolved"] != 4 || edges[0]["unresolved"] != 2 {
		t.Fatalf("edge coverage = %#v", cov.Props["edge_coverage"])
	}
	if causes := cov.Props["unresolved_macros"]; causes != "instantiated_name=1,label_not_literal=1" {
		t.Errorf("causes = %v", causes)
	}
}

// TestTestCaseNeedsFrameworkInclude: a project macro called TEST in a file whose
// includes do not lead to GoogleTest names no test.
func TestTestCaseNeedsFrameworkInclude(t *testing.T) {
	ff := extractProject(t, map[string]string{"src/log.cc": "#include \"log.h\"\n\nTEST(Log, Writes) {\n}\n"})
	if got := testCaseRunners(ff); len(got) != 0 {
		t.Errorf("runners = %v, want none", got)
	}
	if _, ok := findFact(ff, "cpp:test_cases"); ok {
		t.Error("a coverage fact with nothing to count")
	}
}

// TestTestCaseThroughProjectHeader: a test file that reaches GoogleTest only
// through the project's own headers, as opencv's test_precomp.hpp does, is read,
// and so is a suite header such a file includes; a file whose includes never lead
// there is not, whatever its macros are called.
func TestTestCaseThroughProjectHeader(t *testing.T) {
	ff := extractProject(t, map[string]string{
		"modules/ts/include/opencv2/ts.hpp":        "#include \"opencv2/ts/ts_gtest.h\"\n",
		"modules/ts/include/opencv2/ts/ts_gtest.h": "#pragma once\n",
		"modules/core/test/test_precomp.hpp":       "#include \"opencv2/ts.hpp\"\n",
		"modules/core/test/test_mat.cpp":           "#include \"test_precomp.hpp\"\n\nTEST(Core_Mat, Reshape) {\n}\n",
		"modules/imgcodecs/test/test_precomp.hpp":  "#include \"opencv2/ts.hpp\"\n",
		"modules/imgcodecs/test/test_exr.cpp":      "#include \"test_precomp.hpp\"\n#include \"test_exr.impl.hpp\"\n",
		"modules/imgcodecs/test/test_exr.impl.hpp": "TEST(Imgcodecs_EXR, readWrite) {\n}\n",
		"src/dict.c": "#include \"dict.h\"\n#define TEST(name) printf(name)\n\nTEST(Dict, Add) {\n}\n",
		"src/dict.h": "#pragma once\n",
	})
	if got, want := testCaseRunners(ff), []string{"Core_Mat.Reshape", "Imgcodecs_EXR.readWrite"}; !reflect.DeepEqual(got, want) {
		t.Errorf("runners = %v, want %v", got, want)
	}
}

func TestNamesTestFramework(t *testing.T) {
	for include, want := range map[string][2]bool{
		"gtest/gtest.h":                  {true, false},
		"gmock/gmock.h":                  {true, false},
		"opencv2/ts/ts_gtest.h":          {true, false},
		"catch2/catch_test_macros.hpp":   {false, true},
		"third_party/catch2/catch.hpp":   {false, true},
		"catch.hpp":                      {false, true},
		"catch_amalgamated.hpp":          {false, true},
		"util/exception_catcher.h":       {false, false},
		"catcher.h":                      {false, false},
		"gtest_util/../common/strings.h": {false, false},
	} {
		g, c := namesTestFramework(include)
		if [2]bool{g, c} != want {
			t.Errorf("%s: gtest %v catch %v, want %v", include, g, c, want)
		}
	}
}

// TestTestCaseUnderPreprocessorBranch: a test under #if, #ifdef or #else is a
// test the file defines, in every branch, as protobuf's platform tests are; one
// under #if 0 is not, and its #else is.
func TestTestCaseUnderPreprocessorBranch(t *testing.T) {
	src := "#include <gtest/gtest.h>\n\nnamespace {\n#if defined(_WIN32)\nTEST(Io, Windows) {\n}\n#else\nTEST(Io, Posix) {\n}\n#endif\n#ifdef HAVE_ZLIB\nTEST(Io, Gzip) {\n}\n#endif\n#if 0\nTEST(Doc, Example) {\n}\n#else\nTEST(Io, Plain) {\n}\n#endif\n}\n"
	ff := extractProject(t, map[string]string{"io/io_test.cc": src})
	if got, want := testCaseRunners(ff), []string{"Io.Gzip", "Io.Plain", "Io.Posix", "Io.Windows"}; !reflect.DeepEqual(got, want) {
		t.Errorf("runners = %v, want %v", got, want)
	}
}

// TestTestCaseSpanStartsAtTheMacro: a macro on the line before a test, which the
// grammar reads as the test's return type, is not part of the test's span.
func TestTestCaseSpanStartsAtTheMacro(t *testing.T) {
	src := "#include <gtest/gtest.h>\n\nFMT_END_NAMESPACE\n\nTEST(args_test, custom_format) {\n}\n"
	ff := extractProject(t, map[string]string{"test/args-test.cc": src})
	tc := mustFact(t, ff, "test/args-test.cc::args_test.custom_format")
	if span := [4]int{tc.Line, tc.EndLine, tc.Column, tc.EndColumn}; span != [4]int{5, 6, 1, 2} {
		t.Errorf("span = %v, want [5 6 1 2]", span)
	}
}

// TestTestCaseInsideParseError: two test headers sharing one body across #if and
// #else, as googlemock's matcher tests do, is a stretch the grammar cannot parse;
// the definition it keeps whole inside that stretch is still read.
func TestTestCaseInsideParseError(t *testing.T) {
	src := "#include \"gmock/gmock.h\"\n\nnamespace {\n\n#if defined(_MSC_VER) && (_MSC_VER < 1910)\nTEST(ComparisonBaseTest, DISABLED_WorksWithMoveOnly) {\n#else\nTEST(ComparisonBaseTest, WorksWithMoveOnly) {\n#endif\n  EXPECT_TRUE(true);\n}\n\n}  // namespace\n"
	ff := extractProject(t, map[string]string{"test/matchers_test.cc": src})
	if got, want := testCaseRunners(ff), []string{"ComparisonBaseTest.DISABLED_WorksWithMoveOnly"}; !reflect.DeepEqual(got, want) {
		t.Errorf("runners = %v, want %v", got, want)
	}
}
