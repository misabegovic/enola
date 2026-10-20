package goextractor

import (
	"context"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const spanFixture = `package shapes

import (
	"fmt"
)

// Area is documented, and the comment is not part of its span.
func Area(w, h int) int {
	return w * h
}

type Rect struct {
	W, H int
}

func (r *Rect) Scale(k int) {
	r.W *= k
	r.H *= k
}

type Shape interface {
	Area() int
	Describe(prefix string,
		suffix string) string
}

const Pi = 3

var Names = map[string]int{
	"a": 1,
}

func Print() { fmt.Println("x") }
`

type wantSpan struct{ line, endLine, column, endColumn int }

func spanOfFact(f facts.Fact) wantSpan { return wantSpan{f.Line, f.EndLine, f.Column, f.EndColumn} }

// TestSymbolSpanGo pins the span every Go symbol fact carries: from the node's
// start to one past its closing token, with the doc comment outside it. The end
// lines and columns are read from the fixture above.
func TestSymbolSpanGo(t *testing.T) {
	ff := extractAll(t, map[string]string{"shapes/shapes.go": spanFixture})

	want := map[string]wantSpan{
		"shapes.Area":           {8, 10, 1, 2},
		"shapes.Rect":           {12, 14, 6, 2},
		"shapes.Rect.Scale":     {16, 19, 1, 2},
		"shapes.Shape":          {21, 25, 6, 2},
		"shapes.Shape.Area":     {22, 22, 2, 12},
		"shapes.Shape.Describe": {23, 24, 2, 24},
		"shapes.Pi":             {27, 27, 7, 13},
		"shapes.Names":          {29, 31, 5, 2},
		"shapes.Print":          {33, 33, 1, 34},
		"shapes -> fmt":         {4, 4, 2, 7},
	}
	for name, w := range want {
		f, ok := findFact(ff, name)
		if !ok {
			t.Errorf("no fact named %s", name)
			continue
		}
		if got := spanOfFact(f); got != w {
			t.Errorf("%s span = %+v, want %+v", name, got, w)
		}
	}
}

// The facts a Go route registration or client call produces are read from a call
// node and carry its span too, so a caller can show the registration whole.
func TestSymbolSpanGo_RouteCarriesTheCallsSpan(t *testing.T) {
	ff := extractAll(t, map[string]string{"api/routes.go": `package api

import "net/http"

func Register(mux *http.ServeMux) {
	mux.HandleFunc("/health",
		health)
}

func health(w http.ResponseWriter, r *http.Request) {}
`})
	var route facts.Fact
	for _, f := range findFactsByKind(ff, facts.KindRoute) {
		if f.Name == "/health" {
			route = f
		}
	}
	if route.Name == "" {
		t.Fatalf("no /health route among %+v", findFactsByKind(ff, facts.KindRoute))
	}
	if got, w := spanOfFact(route), (wantSpan{6, 7, 2, 10}); got != w {
		t.Errorf("route span = %+v, want %+v", got, w)
	}
}

func TestSymbolSpanGo_SpanStaysOffTheTestRef(t *testing.T) {
	dir := setupGoProject(t, map[string]string{
		"shapes/shapes.go":      spanFixture,
		"shapes/shapes_test.go": "package shapes\n\nimport \"testing\"\n\nfunc TestArea(t *testing.T) {\n\tArea(1, 2)\n}\n",
	})
	ff, err := New().ExtractTestRefs(context.Background(), dir, []string{"shapes/shapes_test.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range ff {
		if f.Kind == facts.KindTestRef && (f.Line != 0 || f.EndLine != 0 || f.Column != 0 || f.EndColumn != 0) {
			t.Errorf("test_ref gained a position: %+v", f)
		}
	}
}
