package cppextractor

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/enola-labs/enola/internal/extractors/tsutil"
	"github.com/enola-labs/enola/internal/facts"
)

// testCaseReading is what one framework's macros gave in one file: the tests
// named, and those counted because no name can be read for them.
type testCaseReading struct {
	cases      []facts.Fact
	unlabelled int
	// instantiated are parameterised and typed tests, named by an instantiation.
	instantiated int
}

// testCandidates are a file's GoogleTest and Catch2 readings, kept apart until the
// project's includes say which framework, if any, the file is compiled against.
type testCandidates struct {
	gtest, catch testCaseReading
}

// gtestFixed are the GoogleTest macros whose test is named Suite.Name by its two
// arguments. The parameterised and typed forms are run under names their
// instantiation builds (Prefix/Suite.Name/0, Suite/0.Name), so they are counted
// rather than named.
var gtestFixed = map[string]bool{"TEST": true, "TEST_F": true}
var gtestInstantiated = map[string]bool{"TEST_P": true, "TYPED_TEST": true, "TYPED_TEST_P": true}

// cppTestCases reads a file's top-level GoogleTest and Catch2 macros. A GoogleTest
// TEST(Suite, Name) is named Suite.Name, as --gtest_filter matches it, and a
// Catch2 TEST_CASE("name", ...) by its name. The grammar reads TEST(...) { } as a
// function definition and TEST_CASE("...") { } as a call statement beside a
// block; the span covers the body either way. Which reading stands is decided
// later, by whether the file reaches the framework's header through its includes.
func cppTestCases(kinds *tsutil.KindTable, root *sitter.Node, src []byte, relFile, lang string) testCandidates {
	var found testCandidates
	found.read(kinds, root, src, relFile, lang)
	return found
}

// read walks one declaration list, and the namespaces and preprocessor branches
// inside it, since a suite is as often in an anonymous namespace as at file scope,
// and a platform's tests sit under an #if. Every branch is read but #if 0: the
// fact is what the file defines, and which branch a build compiles is the
// build's to say.
func (found *testCandidates) read(kinds *tsutil.KindTable, list *sitter.Node, src []byte, relFile, lang string) {
	kids := namedChildrenOf(list)
	for i, n := range kids {
		if n.IsError() {
			// The grammar gives up on a stretch it cannot parse (googletest's own
			// unit test is one ERROR from line 317 to the end) and keeps the
			// definitions inside it whole, so the tests in it are read as anywhere.
			found.read(kinds, n, src, relFile, lang)
			continue
		}
		switch kindOf(kinds, n) {
		case "namespace_definition":
			found.read(kinds, n.ChildByFieldName("body"), src, relFile, lang)
			continue
		case "preproc_if", "preproc_elif":
			// #if 0 is how C++ comments out a block, googletest's documentation
			// examples among them: nothing under it is compiled, only its #else.
			if strings.TrimSpace(nodeText(n.ChildByFieldName("condition"), src)) == "0" {
				if alt := n.ChildByFieldName("alternative"); alt != nil {
					found.read(kinds, alt, src, relFile, lang)
				}
				continue
			}
			found.read(kinds, n, src, relFile, lang)
			continue
		case "preproc_ifdef", "preproc_else", "preproc_elifdef":
			found.read(kinds, n, src, relFile, lang)
			continue
		}
		switch kindOf(kinds, n) {
		case "function_definition":
			decl := n.ChildByFieldName("declarator")
			if decl == nil || kindOf(kinds, decl) != "function_declarator" {
				continue
			}
			macro := nodeText(decl.ChildByFieldName("declarator"), src)
			params := decl.ChildByFieldName("parameters")
			switch {
			case macro == "TEST_CASE" || macro == "TEST_CASE_METHOD":
				// A name that is an identifier makes the macro read as a function
				// with a parameter; it is a Catch2 test all the same.
				found.catch.unlabelled++
			case gtestInstantiated[macro]:
				found.gtest.instantiated++
			case gtestFixed[macro] && params != nil:
				args := namedChildrenOf(params)
				if len(args) != 2 {
					continue
				}
				suite, name := strings.TrimSpace(nodeText(args[0], src)), strings.TrimSpace(nodeText(args[1], src))
				if !cppIdentifier(suite) || !cppIdentifier(name) {
					found.gtest.unlabelled++
					continue
				}
				// The span starts at the macro, not the definition: a project macro
				// on the line before (fmt's FMT_END_NAMESPACE) is read by the
				// grammar as the definition's return type.
				endLine, _, endCol := tsutil.Span(n, src)
				found.gtest.cases = append(found.gtest.cases, facts.NewTestCase(lang, relFile, []string{suite}, name, suite+"."+name,
					int(decl.StartPosition().Row)+1, endLine, tsutil.Column(decl), endCol))
			}
		case "expression_statement":
			if i+1 >= len(kids) || kindOf(kinds, kids[i+1]) != "compound_statement" {
				continue
			}
			call := firstNamedChild(n)
			if call == nil || kindOf(kinds, call) != "call_expression" {
				continue
			}
			macro := nodeText(call.ChildByFieldName("function"), src)
			nameAt := -1
			switch macro {
			case "TEST_CASE":
				nameAt = 0
			case "TEST_CASE_METHOD":
				nameAt = 1
			default:
				continue
			}
			args := namedChildrenOf(call.ChildByFieldName("arguments"))
			if len(args) <= nameAt {
				continue
			}
			name, ok := cppStringLiteral(kinds, args[nameAt], src)
			if !ok {
				found.catch.unlabelled++
				continue
			}
			body := kids[i+1]
			endLine, _, endCol := tsutil.Span(body, src)
			found.catch.cases = append(found.catch.cases, facts.NewTestCase(lang, relFile, nil, name, name,
				int(n.StartPosition().Row)+1, endLine, tsutil.Column(n), endCol))
		}
	}
}

// namesTestFramework reads an include path for GoogleTest's or Catch2's headers
// by name: a gtest or gmock header (gtest/gtest.h, and a project's own wrapper
// such as ts_gtest.h), and Catch2's catch2/ directory or its single headers
// catch.hpp and catch_amalgamated.hpp.
func namesTestFramework(include string) (gtest, catch bool) {
	include = strings.ToLower(include)
	base := path.Base(include)
	gtest = strings.Contains(base, "gtest") || strings.Contains(base, "gmock")
	catch = strings.HasPrefix(include, "catch2/") || strings.Contains(include, "/catch2/") ||
		strings.HasPrefix(base, "catch.") || strings.HasPrefix(base, "catch_amalgamated.")
	return gtest, catch
}

// testFrameworkReach is the files compiled against each framework: those that
// include its header, every file that reaches one of those through project
// includes, and the headers those files include in turn, which are compiled in
// their includer's translation unit (opencv keeps whole suites in .impl.hpp
// headers that include nothing themselves). Includes resolve as the module graph
// resolves them. A project's own macro called TEST (redis defines one to print a
// banner) is never read as GoogleTest's, because nothing around it leads to
// GoogleTest.
func testFrameworkReach(files []string, includes [][]fileInclude, index headerPathIndex) (gtest, catch map[string]bool) {
	gtest, catch = map[string]bool{}, map[string]bool{}
	includers, included := map[string][]string{}, map[string][]string{}
	for i, file := range files {
		for _, inc := range includes[i] {
			g, c := namesTestFramework(inc.path)
			if g {
				gtest[file] = true
			}
			if c {
				catch[file] = true
			}
			if target := index.resolve(file, inc.path, inc.style); target != "" && target != file {
				includers[target] = append(includers[target], file)
				included[file] = append(included[file], target)
			}
		}
	}
	for _, reached := range []map[string]bool{gtest, catch} {
		spread(reached, includers)
		spread(reached, included)
	}
	return gtest, catch
}

func spread(reached map[string]bool, edges map[string][]string) {
	queue := make([]string, 0, len(reached))
	for file := range reached {
		queue = append(queue, file)
	}
	for len(queue) > 0 {
		file := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, next := range edges[file] {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
}

type fileInclude struct {
	path, style string
}

func cppIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func cppStringLiteral(kinds *tsutil.KindTable, n *sitter.Node, src []byte) (string, bool) {
	if n == nil || kindOf(kinds, n) != "string_literal" {
		return "", false
	}
	var b strings.Builder
	for _, part := range namedChildrenOf(n) {
		if kindOf(kinds, part) != "string_content" {
			return "", false
		}
		b.WriteString(nodeText(part, src))
	}
	return b.String(), true
}

func namedChildrenOf(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := uint(0); i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}
