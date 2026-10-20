# Third-party attribution: tree-sitter Ruby grammar

This directory vendors the **tree-sitter Ruby grammar**, with one patch, so the Ruby
extractor can parse Ruby source into an AST. It is third-party code, distributed under a
different license (MIT) than the rest of enola (Apache-2.0).

## Source

- **Project:** [tree-sitter-ruby](https://github.com/tree-sitter/tree-sitter-ruby)
- **License:** MIT — see [LICENSE](./LICENSE) (Copyright (c) 2016 Rob Rix)
- **Version vendored:** release `v0.23.1` (Go module `github.com/tree-sitter/tree-sitter-ruby`
  `v0.23.1`, commit `71bd32fb7607035768799732addba884a37a6210`), the module the extractor
  required until this directory replaced it.
- **Patch:** [`anonymous-forwarding.patch`](./anonymous-forwarding.patch), the
  `grammar.js` and `src/scanner.c` hunks of upstream pull request
  [#291](https://github.com/tree-sitter/tree-sitter-ruby/pull/291) by Jeroen Ketema
  (head `ff2e7673a8f1`, open as of 2026-10-06), which fixes issues
  [#244](https://github.com/tree-sitter/tree-sitter-ruby/issues/244) and
  [#290](https://github.com/tree-sitter/tree-sitter-ruby/issues/290).

## Why it is vendored rather than required

v0.23.1 is the newest release, and it cannot read Ruby 3.1's anonymous block forwarding
when a space or a newline follows the `&`: its external scanner takes any `&` followed by
whitespace for the binary operator. `}, &` at the end of a line (the shape Rails helpers
use to pass a block on after a hash of attributes) and `&` alone before a closing
parenthesis therefore leave an error node, and the error recovery around it drops the
methods, and often the module, the file declares after it. Measured over the 13 Ruby
entries of the corpus on 2026-10-06, 21 of 33,208 files were read in part for this one
construct, losing 38 methods and 10 classes or modules. Upstream has no release carrying
the fix, so the grammar is vendored with the fix applied.

The patch makes the binary `&` an external token (`_bitwise_and`), so the scanner sees
both readings at once: where only a block argument can follow (after a comma or an open
parenthesis) a `&` followed by whitespace is the block argument; where an operand stands
before it, the binary operator wins exactly as before.

## What is vendored, and how it was produced

| File(s) | Origin |
|---|---|
| `src/scanner.c` | The upstream v0.23.1 scanner with the patch's scanner hunk applied. |
| `grammar.json` | **Generated** by the tree-sitter CLI from the upstream v0.23.1 `grammar.js` with the patch's grammar hunk applied. |
| `src/parser.c`, `src/node-types.json` | **Generated** from that `grammar.json` at ABI 14. `node-types.json` is byte for byte upstream's: the new token is an alias of `&` and adds no node type. |
| `src/tree_sitter/*.h` | Copied verbatim from the upstream v0.23.1 release. |
| `anonymous-forwarding.patch` | The patch against v0.23.1, kept as the reviewable record of every hand change in this directory. |

Nothing generated was hand-edited. The generator is deterministic: tree-sitter-cli
v0.24.7 run on the unpatched v0.23.1 `grammar.js` reproduces upstream's committed
`src/parser.c`, `src/grammar.json` and `src/node-types.json` byte for byte, so the
difference between upstream's parser and this one is the patch and nothing else. The
`cgo_parser.c` / `cgo_scanner.c` shims and `binding.go` in this directory are enola's
own glue code (Apache-2.0) and are not part of the upstream grammar.

## Regenerating `src/parser.c`

```sh
cd internal/extractors/rubyextractor/grammar
tree-sitter generate grammar.json --abi 14
```

The ABI (14) is pinned to match `github.com/tree-sitter/go-tree-sitter v0.24.0`. This
was produced with `tree-sitter-cli` v0.24.7, the series upstream's `package.json` names.
To rebuild `grammar.json` itself, apply `anonymous-forwarding.patch` to the v0.23.1
`grammar.js` and run `tree-sitter generate grammar.js --abi 14`.

## Verification

- Upstream's test suite passes in full with the patch: 290 of 290 corpus cases and the 5
  highlight files, with the six forwarding lines pull request #291 adds to the corpus case
  "method call with block argument".
- Over the 52,841 `.rb` files of the 13 Ruby corpus entries, the patched grammar leaves an
  error node in 7 files where v0.23.1 left one in 28. The 21 files it recovers are every
  anonymous-forwarding file and no other; the remaining 7 are other constructs (a heredoc
  opened inside an interpolation inside a heredoc, symbols naming special globals, a
  non-ASCII symbol, ERB saved as `.rb`).
- Of the 11,659 of those files holding a single `&` that is not `&&`, `&.` or `&=`, the
  patched grammar's tree differs from v0.23.1's in exactly those 21 files.

## Known grammar gaps

The heredoc-in-interpolation-in-heredoc case and the special-global and non-ASCII symbols
above still leave an error node, and the Ruby extractor names those files on its
`ruby:parse` fact. Upstream's later scanner fix for heredoc serialization buffer
overflows (commit `ad907a6`, after v0.23.1) is not carried.
