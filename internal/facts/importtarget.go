package facts

import "strings"

// ImportTargets resolves an imports relation whose producer carried the file
// of the symbol it imports (PropTargetFile).
//
// Every reader that turns an imports relation into a module edge walks the
// target up by "/" until it names a module. A directory or a file stem walks
// to its own module; a symbol name does not. A Rust item is named after its
// directory, `<dir>.<ident>`, so the walk cuts the last directory segment off
// and lands on the parent module, or on nothing. Measured over ten Rust
// repositories, not one item import walked to the right module. The carried
// file is what places it.
type ImportTargets struct {
	load  func() []Fact
	files map[string]map[string]bool
}

// NewImportTargets indexes the symbol facts load returns, on first need: a
// snapshot with no carried file never builds the index.
func NewImportTargets(load func() []Fact) *ImportTargets {
	return &ImportTargets{load: load}
}

// Dir reports where an imports relation of dep lands. carried is false when
// dep carries no file, and the reader keeps its own walk; no producer but the
// Rust extractor carries one on a fact holding an imports relation, so every
// other import reads as before. Otherwise dir is the directory of the carried
// file when target names a symbol of dep's repository declared in that file,
// and "" when it does not: a refusal, which leaves no edge.
func (t *ImportTargets) Dir(dep Fact, target string) (dir string, carried bool) {
	file, _ := dep.PropAny(PropTargetFile).(string)
	if file == "" {
		return "", false
	}
	if t.files == nil {
		t.index()
	}
	at := t.files[dep.Repo+"\x00"+target]
	if !at[file] && !(dep.Repo != "" && at[dep.Repo+"/"+file]) {
		return "", true
	}
	return fileDirectory(file), true
}

func (t *ImportTargets) index() {
	t.files = map[string]map[string]bool{}
	for _, f := range t.load() {
		if f.Kind != KindSymbol || f.File == "" {
			continue
		}
		key := f.Repo + "\x00" + f.Name
		if t.files[key] == nil {
			t.files[key] = map[string]bool{}
		}
		t.files[key][f.File] = true
		if f.Repo != "" {
			t.files[key][strings.TrimPrefix(f.File, f.Repo+"/")] = true
		}
	}
}

// CallByFileName names a dependency fact carrying one calls relation to a
// target whose name several files declare, from sourceFile to the declaration
// in targetFile. The name holds all three, so two such facts that differ only
// in the file they carry stay two facts.
func CallByFileName(sourceFile, target, targetFile string) string {
	return sourceFile + " -> " + target + " @ " + targetFile
}
