// Package grammar vendors the tree-sitter Ruby grammar (tree-sitter/tree-sitter-ruby
// v0.23.1) with one patch, so Ruby 3.1's anonymous block forwarding parses when a
// space or a newline follows the `&`.
//
// v0.23.1's scanner reads any `&` followed by whitespace as the binary operator, so
// `}, &` at the end of a line and `&` alone before a closing parenthesis leave an
// error node, and recovery drops what the file declares after it. The patch, carried
// as anonymous-forwarding.patch, is upstream's open pull request #291 (fixes upstream
// issues #244 and #290): the binary `&` becomes an external token, and the scanner
// prefers the block argument only where the binary operator cannot follow. The parser
// is regenerated from the patched grammar.json at ABI 14. Regenerate with:
//
//	cd grammar && tree-sitter generate grammar.json --abi 14
//
// The generated parser (src/parser.c) and the external scanner (src/scanner.c) are
// compiled as separate translation units via the cgo_*.c shims in this directory, NOT
// #include'd together here: both define grammar-local macros (e.g. TOKEN_COUNT), so
// combining them into one translation unit triggers -Wmacro-redefined.
package grammar

// #cgo CFLAGS: -std=c11 -fPIC -I${SRCDIR}/src
// #include "tree_sitter/parser.h"
// const TSLanguage *tree_sitter_ruby(void);
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer for Ruby, suitable for
// sitter.NewLanguage(...).
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_ruby())
}
