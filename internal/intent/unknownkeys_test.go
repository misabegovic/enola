package intent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A constraints file's keys that nothing reads are recorded with the line and
// the entry they sit in: at the top of the file, in a rule and in a
// component. said is a rule key.
func TestUnknownKeysAreNamedWithTheirLine(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, filepath.FromSlash(ConstraintsDirName))
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := `componets: []
components:
  - name: api
    match: ["api/**"]
    matches: ["typo/**"]
rules:
  - id: api-stays-small
    cap: api
    max_members: 3
    said: "the api stays small"
    sayd: "misspelt"
    because: a scratch law.
`
	if err := os.WriteFile(filepath.Join(cdir, "x.yaml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	files, problems, err := LoadConstraintsDir(dir)
	if err != nil || len(problems) > 0 {
		t.Fatalf("load: %v %v", err, problems)
	}
	want := []UnknownKey{
		{File: "enola/constraints/x.yaml", Line: 1, Key: "componets"},
		{File: "enola/constraints/x.yaml", Line: 5, Component: "api", Key: "matches"},
		{File: "enola/constraints/x.yaml", Line: 11, Rule: "api-stays-small", Key: "sayd"},
	}
	if !reflect.DeepEqual(files[0].UnknownKeys, want) {
		t.Errorf("unknown keys = %+v, want %+v", files[0].UnknownKeys, want)
	}
	if got := files[0].Rules[0].Said; got != "the api stays small" {
		t.Errorf("said = %q, want the sentence as written", got)
	}
	facts := CompileFacts(MergeConstraintsFiles(nil, files))
	for _, f := range facts {
		if f.Name == "rule: api-stays-small" && f.PropString("said") != "the api stays small" {
			t.Errorf("the compiled rule carries said = %q", f.PropString("said"))
		}
	}
}

// A declaration's lines: each entry of components and rules by position, the
// location a validation problem names.
func TestEntryLinesAreWhereEachEntryStarts(t *testing.T) {
	got := EntryLines([]byte("components:\n  - name: a\n    match: [a/**]\n  - name: b\n    match: [b/**]\nrules:\n  - id: r\n"))
	want := map[string][]int{"components": {2, 4}, "rules": {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entry lines = %v, want %v", got, want)
	}
}
