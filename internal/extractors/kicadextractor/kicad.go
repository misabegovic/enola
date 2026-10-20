// Package kicadextractor extracts architectural facts from KiCad 6 and later
// projects: boards, schematic sheets, parts and nets.
package kicadextractor

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/enola-labs/enola/internal/extractors/detectnames"
	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/factpath"
	"github.com/enola-labs/enola/internal/facts"
)

// A KiCad project is plain text: s-expression boards and sheets carrying a
// date-stamped format version, and a JSON project file holding the net classes.
// A small reader in this package is enough to read them, so there is no grammar
// dependency and no KiCad install, the hand-rolled tradition the HCL, gRPC and
// OpenAPI extractors keep.
//
// The board is read as the netlist. A schematic stores wires and labels and leaves
// the nets to be solved from geometry, while the board names the net on every
// pad, so reading the board reads KiCad's own answer rather than reimplementing
// the solve. The schematic contributes only its sheet tree and its power symbols.
//
// A footprint is placed on a sheet by its UUID path alone. The sheet name and file
// fields it carries are copied from wherever the footprint came from and can name
// a file outside its own project; a guessed sheet is the edge the fail-closed rule
// forbids. Whatever cannot be read or placed is counted by cause on the extraction
// facts instead.

const language = "kicad"

// The format versions the tests cover: the oldest KiCad 6 file and the newest
// KiCad 10 file the extractor was measured on. A file outside them is refused
// rather than read by rules written for other versions, and the range widens
// only with a fixture.
const (
	oldestVersion = 20211014
	newestVersion = 20260306
)

type Extractor struct{}

func New() *Extractor { return &Extractor{} }

func (e *Extractor) Name() string { return language }

func (e *Extractor) OwnsFile(relFile string) bool {
	switch strings.ToLower(factpath.Ext(relFile)) {
	case ".kicad_pcb", ".kicad_sch", ".kicad_pro":
		return true
	}
	return false
}

func (e *Extractor) Detect(repoPath string) (bool, error) {
	return e.DetectFiles(repoPath, detectnames.Walk(repoPath))
}

// DetectFiles answers on a board or a sheet only: a project file alone, a symbol
// library or a footprint library holds nothing this extractor reads.
func (e *Extractor) DetectFiles(_ string, files []string) (bool, error) {
	for _, rel := range files {
		switch strings.ToLower(factpath.Ext(rel)) {
		case ".kicad_pcb", ".kicad_sch":
			return true, nil
		}
	}
	return false, nil
}

func (e *Extractor) Extract(_ context.Context, repoPath string, files []string) ([]facts.Fact, error) {
	x := &extraction{
		repoPath: repoPath,
		present:  map[string]bool{},
		sheets:   map[string]*sheetFile{},
		reached:  map[string]bool{},
		emitted:  map[string]bool{},
		missing:  map[string]bool{},
		counts: map[string]map[string]int{
			"boards": {}, "pads": {}, "placement": {}, "rails": {}, "classes": {}, "references": {},
		},
		resolved: map[string]int{},
	}
	var boards, schematics []string
	for _, rel := range files {
		rel = factpath.Slash(rel)
		switch strings.ToLower(factpath.Ext(rel)) {
		case ".kicad_pcb":
			boards = append(boards, rel)
		case ".kicad_sch":
			schematics = append(schematics, rel)
		case ".kicad_pro":
		default:
			continue
		}
		x.present[rel] = true
	}
	sort.Strings(boards)
	sort.Strings(schematics)

	for _, board := range boards {
		x.readBoard(board)
	}
	for _, sch := range schematics {
		stem := strings.TrimSuffix(sch, factpath.Ext(sch))
		if x.reached[sch] || x.present[stem+".kicad_pcb"] || !x.present[stem+".kicad_pro"] {
			continue
		}
		tree := x.walk(sch)
		x.emitTree(tree)
		if tree.rootRead {
			x.resolved["boards"]--
			x.counts["boards"]["schematic_without_board"]++
		}
		x.counts["rails"]["on_no_pad"] += len(tree.rails)
		x.counts["rails"]["not_global"] += tree.notGlobal
	}
	for _, sch := range schematics {
		if !x.reached[sch] {
			x.counts["boards"]["outside_any_project"]++
		}
	}

	for _, concern := range []struct{ name, edge string }{
		{"boards", "kicad_file"},
		{"pads", "kicad_pad_net"},
		{"placement", "kicad_part_sheet"},
		{"rails", "kicad_rail"},
		{"classes", "kicad_net_class"},
		{"references", "kicad_reference"},
	} {
		causes := map[string]int{}
		for cause, n := range x.counts[concern.name] {
			if n > 0 {
				causes[cause] = n
			}
		}
		if f, ok := extcoverage.Fact(repoPath, language+":"+concern.name, concern.edge, x.resolved[concern.name], causes); ok {
			x.out = append(x.out, f)
		}
	}
	return x.out, nil
}

type extraction struct {
	repoPath string
	present  map[string]bool
	// sheets holds every schematic file read, by path, so a sheet two projects
	// share is parsed and emitted once; a nil entry is a file that was refused.
	sheets  map[string]*sheetFile
	reached map[string]bool
	emitted map[string]bool
	missing map[string]bool

	resolved map[string]int
	counts   map[string]map[string]int
	out      []facts.Fact
}

type sheetFile struct {
	path string
	// version is kept as the file writes it: a date stamp is an identifier, and
	// read back as a number it prints in exponent form.
	version string
	symbols []sheetSymbol
	rails   []string
	// notGlobal counts power symbols whose marker names a scope other than the
	// global one, whose value therefore names no global net.
	notGlobal int
}

type sheetSymbol struct {
	uuid, name, file string
	pins             map[string]int
	line, endLine    int
	column, endCol   int
}

func (x *extraction) read(path, root string) (*document, string, bool) {
	raw, err := os.ReadFile(filepath.Join(x.repoPath, filepath.FromSlash(path)))
	if err != nil {
		x.counts["boards"]["unreadable"]++
		return nil, "", false
	}
	doc, err := parse(string(raw))
	if err != nil {
		x.counts["boards"]["unparsable"]++
		return nil, "", false
	}
	written, _ := doc.root.first("version").text(1)
	version, err := strconv.Atoi(written)
	if doc.root.head() != root || err != nil || version < oldestVersion || version > newestVersion {
		if written == "" {
			written = "none"
		}
		x.counts["boards"]["unknown_version_"+written]++
		return nil, "", false
	}
	x.resolved["boards"]++
	return doc, written, true
}

type tree struct {
	root     string
	rootRead bool
	files    []string
	// sheetOf maps a sheet symbol's UUID to the file it places; a UUID two sheet
	// symbols share maps to "" and places nothing.
	sheetOf   map[string]string
	rails     map[string]bool
	notGlobal int
}

func (x *extraction) walk(root string) *tree {
	t := &tree{root: root, sheetOf: map[string]string{}, rails: map[string]bool{}}
	if !x.present[root] {
		return t
	}
	queue := []string{root}
	seen := map[string]bool{root: true}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		x.reached[path] = true
		sf := x.sheet(path)
		if sf == nil {
			continue
		}
		if path == root {
			t.rootRead = true
		}
		t.files = append(t.files, path)
		for _, r := range sf.rails {
			t.rails[r] = true
		}
		t.notGlobal += sf.notGlobal
		for _, s := range sf.symbols {
			if prior, dup := t.sheetOf[s.uuid]; dup && prior != s.file {
				t.sheetOf[s.uuid] = ""
			} else if !dup {
				t.sheetOf[s.uuid] = s.file
			}
			if s.file == "" || seen[s.file] {
				continue
			}
			seen[s.file] = true
			if !x.present[s.file] {
				if !x.missing[s.file] {
					x.missing[s.file] = true
					x.counts["boards"]["missing"]++
				}
				continue
			}
			queue = append(queue, s.file)
		}
	}
	return t
}

func (x *extraction) sheet(path string) *sheetFile {
	if sf, done := x.sheets[path]; done {
		return sf
	}
	x.sheets[path] = nil
	doc, version, ok := x.read(path, "kicad_sch")
	if !ok {
		return nil
	}
	sf := &sheetFile{path: path, version: version}
	projectDir := factpath.Dir(path)
	for _, s := range doc.root.each("sheet") {
		file, _ := s.property("Sheetfile", "Sheet file")
		name, _ := s.property("Sheetname", "Sheet name")
		sym := sheetSymbol{uuid: s.firstText("uuid"), name: name, pins: map[string]int{}}
		if resolved := resolveSheetFile(projectDir, file); resolved != "" {
			sym.file = resolved
		}
		for _, p := range s.each("pin") {
			if dirn, ok := p.text(2); ok && dirn != "" {
				sym.pins[dirn]++
			}
		}
		sym.line, sym.column = doc.position(s.start)
		sym.endLine, sym.endCol = doc.position(s.end)
		sym.endCol++
		sf.symbols = append(sf.symbols, sym)
	}
	sf.rails, sf.notGlobal = powerNets(doc.root)
	x.sheets[path] = sf
	return sf
}

// resolveSheetFile reads a relative sheet file against the directory of the sheet
// that places it; in every project measured the two are one directory. A path
// that leaves the repository names nothing this extractor can read.
func resolveSheetFile(dir, file string) string {
	file = factpath.Declared(strings.TrimSpace(file))
	if file == "" || strings.HasPrefix(file, "/") {
		return ""
	}
	resolved := factpath.Clean(factpath.Join(dir, file))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return ""
	}
	return resolved
}

// powerNets names the global nets a sheet's power symbols create. A power symbol
// is recognised by what its embedded library symbol says, the power marker and a
// power-input pin, never by its library name: PWR_FLAG sits in the same library
// with an output pin and names no net.
func powerNets(root *node) ([]string, int) {
	libs := map[string]*node{}
	for _, s := range root.first("lib_symbols").each("symbol") {
		if name, ok := s.text(1); ok {
			libs[name] = s
		}
	}
	names := map[string]bool{}
	notGlobal := 0
	for _, placed := range root.each("symbol") {
		libName := placed.firstText("lib_name")
		if libName == "" {
			libName = placed.firstText("lib_id")
		}
		lib := libs[libName]
		marker := lib.first("power")
		if marker == nil || !hasPin(lib, "power_in") {
			continue
		}
		value, _ := placed.property("Value")
		if value == "" {
			continue
		}
		if scope, _ := marker.text(1); scope != "" && scope != "global" {
			notGlobal++
			continue
		}
		names[value] = true
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, notGlobal
}

func hasPin(n *node, kind string) bool {
	for _, c := range n.list {
		if !c.isList {
			continue
		}
		if c.head() == "pin" {
			if k, _ := c.text(1); k == kind {
				return true
			}
			continue
		}
		if c.head() == "symbol" && hasPin(c, kind) {
			return true
		}
	}
	return false
}

// emitTree writes the modules and the sheet dependencies of a project's tree,
// each file once however many projects reach it, and a dependency only to a
// sheet that was read, so no imports edge points at a module that is not there.
func (x *extraction) emitTree(t *tree) {
	for _, path := range t.files {
		if x.emitted[path] {
			continue
		}
		x.emitted[path] = true
		sf := x.sheets[path]
		x.out = append(x.out, facts.Fact{
			Kind:  facts.KindModule,
			Name:  path,
			File:  path,
			Props: map[string]any{"language": language, "format_version": sf.version},
		})
		for _, s := range sf.symbols {
			if s.file == "" || x.sheets[s.file] == nil {
				continue
			}
			props := map[string]any{"language": language, "source": "internal"}
			if s.name != "" {
				props["sheet_name"] = s.name
			}
			total := 0
			for dirn, n := range s.pins {
				props["pins_"+dirn] = n
				total += n
			}
			props["pins"] = total
			x.out = append(x.out, facts.Fact{
				Kind: facts.KindDependency,
				Name: path + " -> " + s.file,
				File: path,
				Line: s.line, EndLine: s.endLine, Column: s.column, EndColumn: s.endCol,
				Props:     props,
				Relations: []facts.Relation{{Kind: facts.RelImports, Target: s.file}},
			})
		}
	}
}
