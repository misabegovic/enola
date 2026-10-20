package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// memTree is a working tree in memory, with the snapshot's recorded hashes
// taken from the same content unless a test moves a file after recording.
type memTree struct {
	files    map[string]string
	recorded map[string]string
}

func newMemTree(files map[string]string) *memTree {
	t := &memTree{files: files, recorded: map[string]string{}}
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		t.recorded[name] = hex.EncodeToString(sum[:])
	}
	return t
}

func (m *memTree) tree() Tree {
	return Tree{
		ReadFile: func(rel string) ([]byte, error) {
			content, ok := m.files[rel]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(content), nil
		},
		IsDir: func(rel string) bool {
			for name := range m.files {
				if strings.HasPrefix(name, rel+"/") {
					return true
				}
			}
			return false
		},
		Recorded: m.recorded,
	}
}

// testFile builds a file of n tests, each `size` lines long, and the test_case
// facts a Go extractor would emit for it.
func testFile(file string, n, size int) (string, []facts.Fact) {
	var sb strings.Builder
	var ff []facts.Fact
	line := 1
	sb.WriteString("package x\n")
	line++
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("TestCase%03d", i)
		start := line
		fmt.Fprintf(&sb, "func %s(t *testing.T) {\n", name)
		line++
		for j := 0; j < size-2; j++ {
			fmt.Fprintf(&sb, "\tt.Log(%q)\n", strings.Repeat("x", 40))
			line++
		}
		sb.WriteString("}\n")
		ff = append(ff, facts.Fact{Kind: facts.KindTestCase, Name: facts.TestCaseName(file, name), File: file,
			Line: start, EndLine: line, Props: map[string]any{"runner_name": name}})
		line++
	}
	return sb.String(), ff
}

// TestContextPackBudget: at 2,000, 8,000 and 32,000 estimated tokens the counted
// total never exceeds the budget, the tests are taken in order until one does
// not fit, the left-out count is every match after that, and the file too big for
// what remains is marked to search rather than counted.
func TestContextPackBudget(t *testing.T) {
	content, cases := testFile("big_test.go", 200, 30)
	tree := newMemTree(map[string]string{"big_test.go": content})
	perTest := (&fileState{tree: tree.tree(), lines: map[string][]string{}, verdict: map[string]string{}}).tokens("big_test.go", cases[0].Line, cases[0].EndLine)
	for _, budget := range []int{2000, 8000, 32000} {
		report, err := Compute(Request{Paths: []string{"big_test.go"}, Names: []string{"TestCase"}, Budget: budget}, cases, tree.tree())
		if err != nil {
			t.Fatal(err)
		}
		if report.Counted > budget {
			t.Errorf("budget %d: counted %d is over", budget, report.Counted)
		}
		taken := 0
		sum := 0
		for _, it := range report.Items {
			if it.Kind == KindTest && it.Counted {
				taken++
				sum += it.Tokens
			}
		}
		if want := budget / perTest; taken != want {
			t.Errorf("budget %d: took %d tests of %d tokens each, want %d", budget, taken, perTest, want)
		}
		if report.LeftOut != len(cases)-taken {
			t.Errorf("budget %d: left out %d, want %d", budget, report.LeftOut, len(cases)-taken)
		}
		if sum != report.Counted {
			t.Errorf("budget %d: items sum to %d, report counts %d", budget, sum, report.Counted)
		}
		last := report.Items[len(report.Items)-1]
		if last.Kind != KindFile || last.Mark != MarkSearch || last.Counted {
			t.Errorf("budget %d: the whole file should be marked to search, got %+v", budget, last)
		}
	}
	// Taken while they fit: once one test does not fit, a smaller one after it is
	// left out too rather than slipped in, so the answer keeps the order asked.
	mixed := map[string]string{"m_test.go": "package m\nfunc TestHuge(t *T) {\n" + strings.Repeat("\tt.Log(\"0123456789012345678901234567890123456789\")\n", 100) + "}\nfunc TestTiny(t *T) {\n}\n"}
	mixedCases := []facts.Fact{
		{Kind: facts.KindTestCase, Name: "m_test.go::TestHuge", File: "m_test.go", Line: 2, EndLine: 103, Props: map[string]any{"runner_name": "TestHuge"}},
		{Kind: facts.KindTestCase, Name: "m_test.go::TestTiny", File: "m_test.go", Line: 104, EndLine: 105, Props: map[string]any{"runner_name": "TestTiny"}},
	}
	report, err := Compute(Request{Paths: []string{"m_test.go"}, Names: []string{"TestHuge", "TestTiny"}, Budget: 200}, mixedCases, newMemTree(mixed).tree())
	if err != nil {
		t.Fatal(err)
	}
	if report.LeftOut != 2 || report.Counted != 0 {
		t.Errorf("a test after one that did not fit was taken: left out %d, counted %d, items %+v", report.LeftOut, report.Counted, report.Items)
	}
	if _, err := Compute(Request{Paths: []string{"big_test.go"}}, cases, tree.tree()); err == nil {
		t.Error("a request with no budget answered")
	}
	if _, err := Compute(Request{Names: []string{"TestCase"}, Budget: 100}, cases, tree.tree()); err == nil {
		t.Error("a request with no path answered")
	}
}

// TestContextPackOrder: tests in the order their names were asked and then file
// order (the order the paths were named, then line), then symbols the same way,
// then each named file whole; a symbol with no end is listed at its start and not
// counted; a match outside the named paths is not an answer.
func TestContextPackOrder(t *testing.T) {
	files := map[string]string{
		"b/b_test.go": "package b\nfunc TestBeta(t *T) {\n}\nfunc TestAlphaInB(t *T) {\n}\n",
		"a/a_test.go": "package a\nfunc TestAlpha(t *T) {\n}\n",
		"a/a.go":      "package a\nfunc Alpha() {\n}\nfunc AlphaNoEnd() {}\n",
		"c/c_test.go": "package c\nfunc TestAlphaOutside(t *T) {\n}\n",
	}
	tc := func(file, name string, line, end int) facts.Fact {
		return facts.Fact{Kind: facts.KindTestCase, Name: facts.TestCaseName(file, name), File: file, Line: line, EndLine: end,
			Props: map[string]any{"runner_name": name}}
	}
	all := []facts.Fact{
		tc("a/a_test.go", "TestAlpha", 2, 3),
		tc("b/b_test.go", "TestAlphaInB", 4, 5),
		tc("b/b_test.go", "TestBeta", 2, 3),
		tc("c/c_test.go", "TestAlphaOutside", 2, 3),
		{Kind: facts.KindSymbol, Name: "a.AlphaNoEnd", File: "a/a.go", Line: 4},
		{Kind: facts.KindSymbol, Name: "a.Alpha", File: "a/a.go", Line: 2, EndLine: 3},
	}
	report, err := Compute(Request{Paths: []string{"b", "a", "a/a.go"}, Names: []string{"Beta", "Alpha"}, Budget: 10000}, all, newMemTree(files).tree())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range report.Items {
		got = append(got, fmt.Sprintf("%s %s %s %d-%d %s", it.Kind, it.File, it.Name, it.Line, it.EndLine, it.Mark))
	}
	want := []string{
		"test b/b_test.go TestBeta 2-3 ",
		"test b/b_test.go TestAlphaInB 4-5 ",
		"test a/a_test.go TestAlpha 2-3 ",
		"symbol a/a.go a.Alpha 2-3 ",
		"symbol a/a.go a.AlphaNoEnd 4-0 no-span",
		"file a/a.go  1-4 ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got %q\nwant %q", got, want)
	}
	for _, it := range report.Items {
		if it.Mark == MarkNoSpan && (it.Counted || it.Tokens != 0) {
			t.Errorf("a symbol with no end was counted: %+v", it)
		}
	}
	if !strings.Contains(report.Render(), EstimateNote) || !strings.Contains(report.Render(), "0 match(es) left out") {
		t.Errorf("render lacks the estimate or the left-out line:\n%s", report.Render())
	}
}

// TestContextPackStaleFile: a named file whose content moved since the snapshot
// is named with no range, and the files that did not move are answered.
func TestContextPackStaleFile(t *testing.T) {
	files := map[string]string{
		"a_test.go": "package a\nfunc TestA(t *T) {\n}\n",
		"b_test.go": "package b\nfunc TestB(t *T) {\n}\n",
	}
	tree := newMemTree(files)
	tree.files["a_test.go"] = "package a\n\nfunc TestA(t *T) {\n}\n"
	all := []facts.Fact{
		{Kind: facts.KindTestCase, Name: "a_test.go::TestA", File: "a_test.go", Line: 2, EndLine: 3, Props: map[string]any{"runner_name": "TestA"}},
		{Kind: facts.KindTestCase, Name: "b_test.go::TestB", File: "b_test.go", Line: 2, EndLine: 3, Props: map[string]any{"runner_name": "TestB"}},
	}
	report, err := Compute(Request{Paths: []string{"a_test.go", "b_test.go"}, Names: []string{"Test"}, Budget: 1000}, all, tree.tree())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Moved) != 1 || report.Moved[0].File != "a_test.go" || report.Moved[0].Reason != "moved since the snapshot" {
		t.Errorf("moved = %+v, want a_test.go alone", report.Moved)
	}
	for _, it := range report.Items {
		if it.File == "a_test.go" {
			t.Errorf("the moved file got a range: %+v", it)
		}
	}
	var answered []string
	for _, it := range report.Items {
		answered = append(answered, it.Kind+" "+it.File)
	}
	if !reflect.DeepEqual(answered, []string{"test b_test.go", "file b_test.go"}) {
		t.Errorf("answered %v, want the test and the whole of b_test.go", answered)
	}

	delete(tree.recorded, "b_test.go")
	report, _ = Compute(Request{Paths: []string{"b_test.go"}, Names: []string{"TestB"}, Budget: 1000}, all, tree.tree())
	if len(report.Moved) != 1 || report.Moved[0].Reason != "not in the snapshot" || len(report.Items) != 0 {
		t.Errorf("a file the snapshot holds no hash for was answered: %+v", report)
	}
}

// TestContextPackRefusesWithoutSnapshot: with no snapshot, another config, or a
// snapshot with no file hashes, the whole answer is refused, in the words a pin
// reuse uses.
func TestContextPackRefusesWithoutSnapshot(t *testing.T) {
	current := &facts.SnapshotMeta{ConfigHash: "c1", RepoPath: "/r", EnolaVersion: "v", ExtractorVersion: "x"}
	meta := &facts.SnapshotMeta{ConfigHash: "c1", RepoPath: "/r", EnolaVersion: "v", ExtractorVersion: "x", FileHashes: []facts.FileHash{{Path: "a.go", Hash: "h"}}}
	cases := []struct {
		meta *facts.SnapshotMeta
		want string
	}{
		{nil, "repo holds no snapshot"},
		{&facts.SnapshotMeta{ConfigHash: "c2", RepoPath: "/r"}, "repo's snapshot was written under another config"},
		{&facts.SnapshotMeta{ConfigHash: "c1", RepoPath: "/r", EnolaVersion: "v", ExtractorVersion: "x"}, "repo's snapshot carries no file hashes"},
		{meta, ""},
	}
	for _, c := range cases {
		if got := Refusal("repo", c.meta, current); got != c.want {
			t.Errorf("Refusal = %q, want %q", got, c.want)
		}
	}
	if got := Refusal("repo", meta, nil); got != "repo could not be described" {
		t.Errorf("Refusal with no current description = %q", got)
	}
}
