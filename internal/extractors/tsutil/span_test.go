package tsutil

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	c "github.com/tree-sitter/tree-sitter-c/bindings/go"
)

// TestSymbolSpanTreeSitter: a node ends where its last character is, including
// one the grammar closes on the newline after it (C's #define does), which ends
// on the line it touches rather than at column zero of the next.
func TestSymbolSpanTreeSitter(t *testing.T) {
	src := []byte("#define LIMIT 10\nint area(int w) {\n  return w;\n}\n")
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(c.Language())); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()
	root := tree.RootNode()

	define := root.NamedChild(0)
	if define.EndPosition().Column != 0 || define.EndPosition().Row != 1 {
		t.Fatalf("the grammar no longer closes #define on the newline: %v", define.EndPosition())
	}
	if end, col, endCol := Span(define, src); end != 1 || col != 1 || endCol != 17 {
		t.Errorf("#define span = %d %d %d, want 1 1 17", end, col, endCol)
	}
	fn := root.NamedChild(1)
	if end, col, endCol := Span(fn, src); end != 4 || col != 1 || endCol != 2 {
		t.Errorf("function span = %d %d %d, want 4 1 2", end, col, endCol)
	}
	if EndLine(fn, src) != 4 || Column(fn) != 1 || EndColumn(fn, src) != 2 {
		t.Error("the field helpers disagree with Span")
	}
}
