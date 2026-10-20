// Package pack answers "what should I open, within this budget": the ranges of
// the tests and symbols a caller names inside the paths it names, then each
// named file whole, estimated in tokens and never over the budget stated.
//
// It reads a snapshot and the working tree and writes nothing. It is
// deterministic and model-free: the token count is characters divided by four,
// an estimate for every model and labelled so, because every model counts
// differently and a caller's window is its own to know.
package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/facts"
)

// CharsPerToken is the estimate: four characters a token.
const CharsPerToken = 4

// EstimateNote is printed with every answer, so a count is never read as a
// tokenizer's.
const EstimateNote = "tokens are estimated at four characters each"

// Item kinds and marks.
const (
	KindTest   = "test"
	KindSymbol = "symbol"
	KindFile   = "file"

	// MarkSearch is a whole file bigger than what remains of the budget: listed
	// with its size so the caller can search it, and not counted.
	MarkSearch = "search"
	// MarkNoSpan is a symbol whose extractor measured no end: listed at its
	// start line and not counted, since its range would be a guess.
	MarkNoSpan = "no-span"
)

type Request struct {
	Paths  []string
	Names  []string
	Budget int
}

// Tree is what the answer reads beside the snapshot: the working tree's files
// and the hashes the snapshot recorded for them.
type Tree struct {
	// ReadFile reads a repo-relative file as it is now.
	ReadFile func(rel string) ([]byte, error)
	// IsDir reports whether a repo-relative path is a directory now; a named
	// directory scopes names and is not itself answered whole.
	IsDir func(rel string) bool
	// Recorded holds the content hash the snapshot recorded per file, from its
	// production and its test file hashes.
	Recorded map[string]string
}

type Item struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Name    string `json:"name,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Tokens  int    `json:"tokens"`
	Counted bool   `json:"counted"`
	Mark    string `json:"mark,omitempty"`
}

// Moved is a named file the answer gives no range for, and why.
type Moved struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type Report struct {
	Budget   int     `json:"budget"`
	Counted  int     `json:"counted"`
	Items    []Item  `json:"items"`
	Moved    []Moved `json:"moved,omitempty"`
	LeftOut  int     `json:"left_out"`
	Estimate string  `json:"estimate"`
}

// Compute builds the answer from the snapshot's facts, which must be the
// repository's own (repo-relative files). Matching follows the request: a name
// matches a test_case when it is a substring of its runner_name, and a symbol
// when it is contained in the symbol's name, only inside the named paths.
// Tests come first in the order their names were asked and then file order,
// then symbols the same way, then each named file whole. Tests and symbols are
// taken while they fit; the first that does not fit and every match after it
// are left out and counted. A symbol with no measured end is listed at its
// start line and not counted. A whole file bigger than what remains is marked
// to search and not counted. A file whose content moved since the snapshot, or
// that the snapshot holds no hash for, is named with no range.
func Compute(req Request, all []facts.Fact, tree Tree) (*Report, error) {
	if req.Budget <= 0 {
		return nil, fmt.Errorf("a budget in estimated tokens is required")
	}
	if len(req.Paths) == 0 {
		return nil, fmt.Errorf("at least one path is required: names are matched only inside the paths given")
	}
	paths := make([]string, len(req.Paths))
	for i, p := range req.Paths {
		paths[i] = cleanPath(p)
	}

	report := &Report{Budget: req.Budget, Estimate: EstimateNote}
	files := &fileState{tree: tree, lines: map[string][]string{}, verdict: map[string]string{}}
	movedNamed := map[string]bool{}
	moved := func(file string) bool {
		reason := files.check(file)
		if reason == "" {
			return false
		}
		if !movedNamed[file] {
			movedNamed[file] = true
			report.Moved = append(report.Moved, Moved{File: file, Reason: reason})
		}
		return true
	}

	var tests, symbols []facts.Fact
	for _, f := range all {
		rank := pathRank(paths, f.File)
		if rank < 0 {
			continue
		}
		switch f.Kind {
		case facts.KindTestCase:
			tests = append(tests, f)
		case facts.KindSymbol:
			symbols = append(symbols, f)
		}
	}

	remaining := req.Budget
	stopped := false
	seen := map[string]bool{}
	take := func(kind string, f facts.Fact) {
		key := fmt.Sprintf("%s\x00%d\x00%d", f.File, f.Line, f.EndLine)
		if seen[key] {
			return
		}
		seen[key] = true
		if moved(f.File) {
			return
		}
		item := Item{Kind: kind, File: f.File, Name: displayName(f), Line: f.Line, EndLine: f.EndLine}
		if f.EndLine == 0 || f.Line == 0 {
			item.Mark = MarkNoSpan
			report.Items = append(report.Items, item)
			return
		}
		if stopped {
			report.LeftOut++
			return
		}
		item.Tokens = files.tokens(f.File, f.Line, f.EndLine)
		if item.Tokens > remaining {
			stopped = true
			report.LeftOut++
			return
		}
		item.Counted = true
		remaining -= item.Tokens
		report.Counted += item.Tokens
		report.Items = append(report.Items, item)
	}

	for _, name := range req.Names {
		for _, f := range inFileOrder(tests, paths) {
			if runner, _ := f.PropAny("runner_name").(string); name != "" && strings.Contains(runner, name) {
				take(KindTest, f)
			}
		}
	}
	for _, name := range req.Names {
		for _, f := range inFileOrder(symbols, paths) {
			if name != "" && strings.Contains(f.Name, name) {
				take(KindSymbol, f)
			}
		}
	}

	for _, p := range paths {
		if p == "." || tree.IsDir(p) {
			continue
		}
		if moved(p) {
			continue
		}
		lines := files.read(p)
		item := Item{Kind: KindFile, File: p, Line: 1, EndLine: len(lines)}
		item.Tokens = files.tokens(p, 1, len(lines))
		if item.Tokens > remaining {
			item.Mark = MarkSearch
			item.Line, item.EndLine = 0, 0
			report.Items = append(report.Items, item)
			continue
		}
		item.Counted = true
		remaining -= item.Tokens
		report.Counted += item.Tokens
		report.Items = append(report.Items, item)
	}
	return report, nil
}

// refusingKinds are the comparability caveats that refuse a whole answer: the
// ones `enola check` treats as blocking, less the provider set, which a pin
// reuse ignores for the same reason (an equal config hash already proves the
// same providers are configured). pkg/command's test holds the two lists equal.
var refusingKinds = map[diff.WarningKind]bool{
	diff.WarnDifferentRepo:   true,
	diff.WarnRepoLabel:       true,
	diff.WarnVersionMismatch: true,
	diff.WarnExtractorSet:    true,
	diff.WarnIgnoreGlobs:     true,
	diff.WarnUnionMembership: true,
	diff.WarnUnclassified:    true,
}

// RefusingKinds lists refusingKinds, for the test that holds it to check's.
func RefusingKinds() []diff.WarningKind {
	out := make([]diff.WarningKind, 0, len(refusingKinds))
	for k := range refusingKinds {
		out = append(out, k)
	}
	return out
}

// Refusal names why a snapshot cannot answer for the tree as it is described
// now, in the words a pin reuse uses, or "" when it can. label names the
// repository in the reason. A snapshot with no file hashes is refused whole:
// without them no file can be vouched for.
func Refusal(label string, meta, current *facts.SnapshotMeta) string {
	if meta == nil {
		return label + " holds no snapshot"
	}
	if current == nil {
		return label + " could not be described"
	}
	if meta.ConfigHash != current.ConfigHash {
		return label + "'s snapshot was written under another config"
	}
	for _, kind := range diff.CompareMeta(*meta, *current).Kinds {
		if refusingKinds[kind] {
			return label + "'s snapshot is not comparable with this build: " + string(kind)
		}
	}
	if len(meta.FileHashes) == 0 {
		return label + "'s snapshot carries no file hashes"
	}
	return ""
}

// Recorded folds a snapshot's production and test file hashes into one map.
func Recorded(meta *facts.SnapshotMeta) map[string]string {
	out := make(map[string]string, len(meta.FileHashes)+len(meta.TestFileHashes))
	for _, fh := range meta.FileHashes {
		out[fh.Path] = fh.Hash
	}
	for _, fh := range meta.TestFileHashes {
		out[fh.Path] = fh.Hash
	}
	return out
}

type fileState struct {
	tree    Tree
	lines   map[string][]string
	verdict map[string]string
}

func (s *fileState) check(file string) string {
	if v, ok := s.verdict[file]; ok {
		return v
	}
	v := ""
	recorded, ok := s.tree.Recorded[file]
	data, err := s.tree.ReadFile(file)
	switch {
	case err != nil && ok:
		v = "gone since the snapshot"
	case err != nil:
		v = "no such file"
	case !ok:
		v = "not in the snapshot"
	default:
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != recorded {
			v = "moved since the snapshot"
		}
	}
	s.verdict[file] = v
	return v
}

func (s *fileState) read(file string) []string {
	if lines, ok := s.lines[file]; ok {
		return lines
	}
	data, err := s.tree.ReadFile(file)
	var lines []string
	if err == nil && len(data) > 0 {
		lines = strings.SplitAfter(string(data), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	}
	s.lines[file] = lines
	return lines
}

// tokens estimates the lines start through end, newlines included.
func (s *fileState) tokens(file string, start, end int) int {
	lines := s.read(file)
	chars := 0
	for i := start; i <= end && i <= len(lines); i++ {
		if i >= 1 {
			chars += utf8.RuneCountInString(lines[i-1])
		}
	}
	return (chars + CharsPerToken - 1) / CharsPerToken
}

func cleanPath(p string) string {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, "\\", "/")), "./")
	return strings.TrimSuffix(p, "/")
}

// pathRank is the index of the first named path a file is, or lies under, and
// -1 when it is in none.
func pathRank(paths []string, file string) int {
	for i, p := range paths {
		if p == "." || file == p || strings.HasPrefix(file, p+"/") {
			return i
		}
	}
	return -1
}

// inFileOrder orders facts by the path that named them, then file, then line,
// so an answer lists what was asked for in the order it was asked.
func inFileOrder(ff []facts.Fact, paths []string) []facts.Fact {
	out := append([]facts.Fact(nil), ff...)
	less := func(a, b facts.Fact) bool {
		ra, rb := pathRank(paths, a.File), pathRank(paths, b.File)
		if ra != rb {
			return ra < rb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	}
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func displayName(f facts.Fact) string {
	if f.Kind == facts.KindTestCase {
		if runner, _ := f.PropAny("runner_name").(string); runner != "" {
			return runner
		}
	}
	return f.Name
}

// Render is the text answer: one line per item, the moved files, the counted
// total against the budget with the estimate named, and the left-out count.
func (r *Report) Render() string {
	var sb strings.Builder
	for _, it := range r.Items {
		switch {
		case it.Mark == MarkNoSpan:
			fmt.Fprintf(&sb, "%s:%d  %s %s  no span measured; start line only, not counted\n", it.File, it.Line, it.Kind, it.Name)
		case it.Mark == MarkSearch:
			fmt.Fprintf(&sb, "%s  whole file  ~%d tokens, more than remains: search it, not counted\n", it.File, it.Tokens)
		case it.Kind == KindFile && it.EndLine == 0:
			fmt.Fprintf(&sb, "%s  whole file  empty\n", it.File)
		case it.Kind == KindFile:
			fmt.Fprintf(&sb, "%s:%d-%d  whole file  ~%d tokens\n", it.File, it.Line, it.EndLine, it.Tokens)
		default:
			fmt.Fprintf(&sb, "%s:%d-%d  %s %s  ~%d tokens\n", it.File, it.Line, it.EndLine, it.Kind, it.Name, it.Tokens)
		}
	}
	for _, m := range r.Moved {
		fmt.Fprintf(&sb, "%s  %s: no range\n", m.File, m.Reason)
	}
	fmt.Fprintf(&sb, "counted ~%d of %d tokens (%s)\n", r.Counted, r.Budget, r.Estimate)
	fmt.Fprintf(&sb, "%d match(es) left out\n", r.LeftOut)
	return sb.String()
}

// JSON is the answer as data, the shape the MCP tool returns.
func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
