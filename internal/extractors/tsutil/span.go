package tsutil

import sitter "github.com/tree-sitter/go-tree-sitter"

// Span is a node's span in the model's terms: the one-based line it ends on, the
// one-based column it starts at, and the column one past its last character.
//
// Some grammars close a node on the newline after its last character, so its end
// point is column zero of the next line. That is read as ending on the line the
// node last touches, one past its last character, so a span never reaches into a
// line the node has no text on. src is the file the node was parsed from.
func Span(n *sitter.Node, src []byte) (endLine, column, endColumn int) {
	start, end := n.StartPosition(), n.EndPosition()
	column = int(start.Column) + 1
	if end.Column > 0 || end.Row == start.Row {
		return int(end.Row) + 1, column, int(end.Column) + 1
	}
	last := int(n.EndByte())
	for last > int(n.StartByte()) && last <= len(src) && (src[last-1] == '\n' || src[last-1] == '\r') {
		last--
	}
	lineStart := last
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	rows := 0
	for i := last; i < int(n.EndByte()) && i < len(src); i++ {
		if src[i] == '\n' {
			rows++
		}
	}
	return int(end.Row) + 1 - rows, column, last - lineStart + 1
}

// EndLine, Column and EndColumn are Span's three parts, for a fact literal that
// sets them field by field.
func EndLine(n *sitter.Node, src []byte) int {
	end, _, _ := Span(n, src)
	return end
}

func Column(n *sitter.Node) int { return int(n.StartPosition().Column) + 1 }

func EndColumn(n *sitter.Node, src []byte) int {
	_, _, endCol := Span(n, src)
	return endCol
}
