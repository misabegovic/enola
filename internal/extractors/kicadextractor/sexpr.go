package kicadextractor

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type node struct {
	isList bool
	list   []*node
	atom   string
	// quoted separates KiCad 10's inline net name (net "5") from the net number an
	// older board writes bare (net 5): the two spell the same text.
	quoted bool
	// start is the offset of the opening parenthesis or of the atom; end is the
	// offset of the closing parenthesis, so a span can end on it.
	start, end int
}

type document struct {
	root       *node
	lineStarts []int
}

func parse(text string) (*document, error) {
	var (
		stack []*node
		roots []*node
	)
	push := func(n *node) {
		if len(stack) == 0 {
			roots = append(roots, n)
			return
		}
		top := stack[len(stack)-1]
		top.list = append(top.list, n)
	}
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case ' ', '\t', '\r', '\n':
			i++
		case '(':
			n := &node{isList: true, start: i}
			push(n)
			stack = append(stack, n)
			i++
		case ')':
			if len(stack) == 0 {
				return nil, fmt.Errorf("unmatched ) at offset %d", i)
			}
			stack[len(stack)-1].end = i
			stack = stack[:len(stack)-1]
			i++
		case '"':
			var b strings.Builder
			j := i + 1
			closed := false
			for j < len(text) {
				ch := text[j]
				if ch == '"' {
					closed = true
					break
				}
				if ch == '\\' && j+1 < len(text) {
					j++
					switch text[j] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					default:
						b.WriteByte(text[j])
					}
					j++
					continue
				}
				b.WriteByte(ch)
				j++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string at offset %d", i)
			}
			push(&node{atom: b.String(), quoted: true, start: i, end: j})
			i = j + 1
		default:
			j := i
			for j < len(text) && !strings.ContainsRune(" \t\r\n()", rune(text[j])) {
				j++
			}
			push(&node{atom: text[i:j], start: i, end: j})
			i = j
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("list opened at offset %d is never closed", stack[0].start)
	}
	if len(roots) != 1 || !roots[0].isList {
		return nil, errors.New("not a single s-expression list")
	}
	doc := &document{root: roots[0], lineStarts: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			doc.lineStarts = append(doc.lineStarts, i+1)
		}
	}
	return doc, nil
}

func (d *document) position(offset int) (line, column int) {
	i := sort.Search(len(d.lineStarts), func(k int) bool { return d.lineStarts[k] > offset }) - 1
	return i + 1, offset - d.lineStarts[i] + 1
}

func (n *node) head() string {
	if n == nil || !n.isList || len(n.list) == 0 || n.list[0].isList {
		return ""
	}
	return n.list[0].atom
}

func (n *node) arg(i int) *node {
	if n == nil || !n.isList || i >= len(n.list) {
		return nil
	}
	return n.list[i]
}

func (n *node) text(i int) (string, bool) {
	a := n.arg(i)
	if a == nil || a.isList {
		return "", false
	}
	return a.atom, true
}

func (n *node) first(head string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.list {
		if c.head() == head {
			return c
		}
	}
	return nil
}

func (n *node) each(head string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, c := range n.list {
		if c.head() == head {
			out = append(out, c)
		}
	}
	return out
}

func (n *node) firstText(head string) string {
	v, _ := n.first(head).text(1)
	return v
}

// property reads a (property NAME VALUE ...) child. The name is quoted in most
// files and bare in some (KiCad 9 writes property ki_fp_filters "..."), and both
// are the same field.
func (n *node) property(names ...string) (string, bool) {
	for _, p := range n.each("property") {
		name, ok := p.text(1)
		if !ok {
			continue
		}
		for _, want := range names {
			if name == want {
				return p.text(2)
			}
		}
	}
	return "", false
}
