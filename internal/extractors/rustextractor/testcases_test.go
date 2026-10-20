package rustextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanRust: an item runs from its first token (visibility included,
// its attributes not) to its closing brace or semicolon, and a use declaration's
// dependency carries the declaration's span.
func TestSymbolSpanRust(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"demo\"\n",
		"src/lib.rs": "use std::fmt;\n\n#[derive(Debug)]\npub struct Rect {\n    pub w: i32,\n}\n\nimpl Rect {\n    pub fn area(&self) -> i32 {\n        self.w\n    }\n}\n\npub const SIDE: i32 = 4;\n",
	})
	got := map[string][4]int{}
	for _, f := range ff {
		got[f.Kind+" "+f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
	}
	want := map[string][4]int{
		"symbol src.Rect":            {4, 6, 1, 2},
		"symbol src.Rect.area":       {9, 11, 5, 6},
		"symbol src.SIDE":            {14, 14, 1, 25},
		"dependency src -> std::fmt": {1, 1, 1, 14},
	}
	for key, span := range want {
		if got[key] != span {
			t.Errorf("%s span = %v, want %v", key, got[key], span)
		}
	}
}

func testRunners(ff []facts.Fact) []string {
	var out []string
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			out = append(out, f.File+" "+f.Props["runner_name"].(string))
		}
	}
	sort.Strings(out)
	return out
}

// TestTestCaseFactsRust: each #[test]-style function is named as cargo test
// lists it, the module path from its target's root through the file's inline
// modules; a comment between the attribute and the function does not separate
// them, and a helper with no test attribute is no test.
func TestTestCaseFactsRust(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          "[package]\nname = \"geometry\"\n",
		"src/lib.rs":          "pub mod shapes;\n\n#[cfg(test)]\nmod tests {\n    fn helper() {}\n\n    #[test]\n    fn doubles() { helper(); }\n\n    #[test]\n    // a comment\n    #[should_panic]\n    fn overflows() {}\n\n    mod nested {\n        #[tokio::test]\n        async fn deep() {}\n    }\n}\n",
		"src/shapes.rs":       "pub mod rect;\n",
		"src/shapes/rect.rs":  "#[cfg(test)]\nmod tests {\n    #[test]\n    fn area() {}\n}\n",
		"tests/area.rs":       "mod common;\n\n#[test]\nfn integration() {}\n\nfn not_a_test() {}\n",
		"tests/common/mod.rs": "#[test]\nfn unit_is_square() {}\n",
	})
	want := []string{
		"src/lib.rs tests::doubles",
		"src/lib.rs tests::nested::deep",
		"src/lib.rs tests::overflows",
		"src/shapes/rect.rs shapes::rect::tests::area",
		"tests/area.rs integration",
		"tests/common/mod.rs common::unit_is_square",
	}
	if got := testRunners(ff); !reflect.DeepEqual(got, want) {
		t.Errorf("runners:\n got %v\nwant %v", got, want)
	}
	for _, f := range ff {
		if f.Kind == facts.KindTestCase && f.Name == "src/lib.rs::tests::overflows" {
			if span := [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}; span != [4]int{13, 13, 5, 22} {
				t.Errorf("overflows span = %v", span)
			}
			if groups, _ := f.Props["groups"].([]string); !reflect.DeepEqual(groups, []string{"tests"}) {
				t.Errorf("groups = %v", groups)
			}
		}
	}
}

// TestTestCaseRust_OutsideModuleTree: a test in a file no module declares has no
// place in any target, so it gets no name and is counted.
func TestTestCaseRust_OutsideModuleTree(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":    "[package]\nname = \"demo\"\n",
		"src/lib.rs":    "pub fn hi() {}\n",
		"src/orphan.rs": "#[test]\nfn lost() {}\n",
	})
	if got := testRunners(ff); len(got) != 0 {
		t.Errorf("runners = %v, want none", got)
	}
	var cov *facts.Fact
	for i := range ff {
		if ff[i].Name == "rust:test_cases" {
			cov = &ff[i]
		}
	}
	if cov == nil || cov.Props["unresolved_macros"] != "outside_module_tree=1" {
		t.Errorf("coverage = %+v", cov)
	}
}

// TestTestCaseRust_Parameterised: a function under #[test_case(...)] or #[rstest]
// is listed once per case under a name its case builds, so it gets no fact and
// is counted.
func TestTestCaseRust_Parameterised(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"demo\"\n",
		"src/lib.rs": "#[cfg(test)]\nmod tests {\n    use test_case::test_case;\n\n    #[test_case(\"a\" ; \"toml\")]\n    #[test_case(\"b\" ; \"yaml\")]\n    fn parses(s: &str) {}\n\n    #[rstest::rstest]\n    fn cases() {}\n\n    #[test]\n    fn plain() {}\n}\n",
	})
	if got, want := testRunners(ff), []string{"src/lib.rs tests::plain"}; !reflect.DeepEqual(got, want) {
		t.Errorf("runners = %v, want %v", got, want)
	}
	for _, f := range ff {
		if f.Name == "rust:test_cases" && f.Props["unresolved_macros"] != "parameterised_name=2" {
			t.Errorf("coverage = %v", f.Props["unresolved_macros"])
		}
	}
}
