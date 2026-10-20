package goextractor

import (
	"go/ast"
	"go/token"

	"github.com/enola-labs/enola/internal/facts"
)

// goSpan is the part of a node's position a fact's Line does not already carry, in
// the model's terms: the one-based column the node starts at, and the line and the
// column one past where it ends.
type goSpan struct{ endLine, column, endColumn int }

func spanOf(fset *token.FileSet, n ast.Node) goSpan {
	return spanBetween(fset, n.Pos(), n.End())
}

// spanBetween exists for the facts whose Line is read from one node and whose end
// from another: a type's line is its name, and its end is the closing brace of the
// type it names.
func spanBetween(fset *token.FileSet, start, end token.Pos) goSpan {
	s, e := fset.Position(start), fset.Position(end)
	return goSpan{endLine: e.Line, column: s.Column, endColumn: e.Column}
}

func (s goSpan) set(f *facts.Fact) {
	f.EndLine, f.Column, f.EndColumn = s.endLine, s.column, s.endColumn
}
