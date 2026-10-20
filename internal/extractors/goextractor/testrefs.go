package goextractor

import (
	"context"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/enola-labs/enola/internal/factpath"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/parallel"
)

// isGoTestFile reports whether a repo-relative path is a Go test file. The Go
// toolchain defines the suffix, so this needs no directory scoping — a production
// file cannot legally be named *_test.go and still compile into the package.
func isGoTestFile(relFile string) bool { return strings.HasSuffix(relFile, "_test.go") }

// ExtractTestRefs implements plugin.TestRefExtractor. It parses *_test.go files for
// the SOLE purpose of capturing their outbound references into production code,
// emitting one facts.KindTestRef fact per file that carries only RelCalls edges —
// no symbols. Test functions therefore never become dead-code candidates, and no
// symbol/module/route explainer is affected, while the dead-code detector can see
// that a production function is exercised by a test and not mis-report it as dead.
// Each test the go command would run is a facts.KindTestCase beside it, which
// carries a span and no edges, so the same holds for it.
//
// The engine hands every TestGlob match to every TestRefExtractor whose repo it
// detected, scoped by plugin.FileOwner when the extractor implements it.
// GoExtractor deliberately does not: FileOwner is what opts an extractor into the
// incremental cache (see its doc comment), and implementing it here would both
// enable Go caching and pull .go files out of computeExtractorKeys' shared
// partition, changing the shared hash that keys EVERY other extractor. So the
// filter lives here instead — Ruby's ExtractTestRefs filters internally too.
// prodFiles is unused: Go call targets resolve lexically through the module path
// and each file's own imports, so this pass needs no view of which production
// files exist.
func (e *GoExtractor) ExtractTestRefs(ctx context.Context, repoPath string, testFiles, _ []string) ([]facts.Fact, error) {
	var goFiles []string
	for _, relFile := range testFiles {
		if isGoTestFile(relFile) {
			goFiles = append(goFiles, relFile)
		}
	}
	if len(goFiles) == 0 {
		return nil, nil
	}

	modulePath := readModulePath(repoPath)
	perFile := parallel.MapFiles(ctx, goFiles, func(relFile string) []facts.Fact {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			log.Printf("[go-extractor] error reading test file %s: %v", relFile, err)
			return nil
		}
		return refsFromGoTest(src, relFile, modulePath)
	})

	var out []facts.Fact
	for _, ff := range perFile {
		out = append(out, ff...)
	}
	return out, nil
}

// refsFromGoTest parses one Go test file and returns a single reference-only fact
// carrying the production symbols it calls, when it calls any, and a test_case per
// test go test would run from it.
//
// Call targets are resolved with the PRODUCTION resolvers (flattenSelector,
// collectLocalTypes, resolveChain), so a reference from a test is spelled exactly
// as the same reference from production code would be, and the dead-code detector
// needs no special case. That also inherits goBuiltins filtering, so len/make/min
// never become phantom targets.
//
// Only call expressions yield targets — matching analyzeBody, which likewise
// ignores composite literals. A type constructed only as `Foo{}` from a test is
// therefore still reported dead, but so is one constructed only that way from
// production code: that blind spot is pre-existing and not specific to tests.
func refsFromGoTest(src []byte, relFile, modulePath string) []facts.Fact {
	fset := token.NewFileSet()
	// Comments are parsed for go/doc, which reads an example's output comment to decide
	// whether go test runs it.
	f, err := parser.ParseFile(fset, relFile, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		log.Printf("[go-extractor] error parsing test file %s: %v", relFile, err)
		return nil
	}

	base := resolveCtx{
		pkgDir:     factpath.Dir(relFile),
		modulePath: modulePath,
		// pkgNames is deliberately nil. It exists to recover a declared package name
		// that differs from its directory base ("go-auth" → package auth), which
		// needs a view of every parsed package — and this pass sees only test files.
		// Worse, a test file's own package name carries a _test suffix
		// (`package svc_test`), so feeding these in would alias the import under
		// test as "svc_test" and break the very idiom this exists to resolve.
		// buildFileImports then falls back to the import path's base, exactly as the
		// production pass does for any package it did not parse.
		imports: buildFileImports(f, modulePath, nil),
	}

	seen := make(map[string]bool)
	var rels []facts.Relation
	add := func(target string) {
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		rels = append(rels, facts.Relation{Kind: facts.RelCalls, Target: target})
	}
	collectCalls := func(n ast.Node, ctx resolveCtx) {
		ast.Inspect(n, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if chain := flattenSelector(call.Fun); chain != nil {
					add(resolveChain(chain, ctx))
				}
			}
			return true
		})
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			ctx := base
			if d.Recv != nil && len(d.Recv.List) > 0 {
				field := d.Recv.List[0]
				ctx.recvType = typeExprToString(field.Type)
				if len(field.Names) > 0 {
					ctx.recvVar = field.Names[0].Name
				}
			}
			ctx.localTypes = collectLocalTypes(d.Body, ctx)
			collectCalls(d.Body, ctx)
		case *ast.GenDecl:
			// File-scope initializers (`var _ = Register(handler)`) reference
			// production code with no enclosing function to attribute them to.
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, v := range vs.Values {
					collectCalls(v, base)
				}
			}
		}
	}

	var out []facts.Fact
	if len(rels) > 0 {
		out = append(out, facts.Fact{
			Kind:      facts.KindTestRef,
			Name:      relFile,
			File:      relFile,
			Props:     map[string]any{"language": "go"},
			Relations: rels,
		})
	}
	return append(out, goTestCases(fset, f, relFile)...)
}

// goTestCases emits one test_case per function go test runs from this file, chosen
// by the rules the go command applies when it builds the test main
// (cmd/go/internal/load/test.go): a Test, Benchmark or Fuzz function whose suffix
// does not start with a lower-case letter and whose one parameter is *T, *B or *F,
// and an example go/doc finds with an output comment, since go test compiles the
// others and never runs them. TestMain taking *testing.M is the package's main, not
// a test. A function the go command would reject (type parameters, results, a
// second parameter) fails the build rather than running, so it gets no fact.
func goTestCases(fset *token.FileSet, f *ast.File, relFile string) []facts.Fact {
	runnableExamples := map[string]bool{}
	for _, ex := range doc.Examples(f) {
		if ex.Output == "" && !ex.EmptyOutput {
			continue
		}
		runnableExamples["Example"+ex.Name] = true
	}

	var out []facts.Fact
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		style := goTestStyle(fn, runnableExamples)
		if style == "" {
			continue
		}
		name := fn.Name.Name
		tc := facts.Fact{
			Kind: facts.KindTestCase,
			Name: facts.TestCaseName(relFile, name),
			File: relFile,
			Line: fset.Position(fn.Pos()).Line,
			Props: map[string]any{
				"language":    "go",
				"label":       name,
				"groups":      []string{},
				"runner_name": name,
				"style":       style,
			},
		}
		spanOf(fset, fn).set(&tc)
		out = append(out, tc)
	}
	return out
}

func goTestStyle(fn *ast.FuncDecl, runnableExamples map[string]bool) string {
	name := fn.Name.Name
	if fn.Type.TypeParams.NumFields() > 0 {
		return ""
	}
	switch {
	case runnableExamples[name]:
		return "example"
	case name == "TestMain":
		if goTestSignature(fn, "T") {
			return "test"
		}
		return ""
	case goTestPrefix(name, "Test") && goTestSignature(fn, "T"):
		return "test"
	case goTestPrefix(name, "Benchmark") && goTestSignature(fn, "B"):
		return "benchmark"
	case goTestPrefix(name, "Fuzz") && goTestSignature(fn, "F"):
		return "fuzz"
	}
	return ""
}

// goTestPrefix is the go command's isTest: "Test" alone is a test, and so is any
// name whose suffix does not start with a lower-case letter, so TestX and Test_x
// run while Testify does not.
func goTestPrefix(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// goTestSignature is the go command's isTestFunc: no results and exactly one
// parameter of type *T or *pkg.T. It cannot check the package is testing, since the
// import may be renamed, and neither can the go command.
func goTestSignature(fn *ast.FuncDecl, arg string) bool {
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	params := fn.Type.Params
	if params == nil || len(params.List) != 1 || len(params.List[0].Names) > 1 {
		return false
	}
	ptr, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := ptr.X.(type) {
	case *ast.Ident:
		return x.Name == arg
	case *ast.SelectorExpr:
		return x.Sel.Name == arg
	}
	return false
}
