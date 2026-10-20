package kicadextractor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/enola-labs/enola/internal/factpath"
	"github.com/enola-labs/enola/internal/facts"
)

type footprint struct {
	reference, value, name, uuid string
	path                         []string
	fields                       map[string]string
	nets                         map[string]bool
	line, endLine                int
	column, endCol               int
}

// notFields are the footprint properties that are not an author's: the three
// the part is named and described by, and the sheet fields, which are read from
// nowhere because the UUID path places the part.
var notFields = map[string]bool{
	"Reference": true, "Value": true, "Footprint": true,
	"Sheetfile": true, "Sheetname": true, "Sheet file": true, "Sheet name": true,
}

func (x *extraction) readBoard(path string) {
	stem := strings.TrimSuffix(path, factpath.Ext(path))
	root := stem + ".kicad_sch"
	doc, version, ok := x.read(path, "kicad_pcb")
	t := x.walk(root)
	x.emitTree(t)
	x.counts["rails"]["not_global"] += t.notGlobal
	if !ok {
		x.counts["rails"]["board_not_read"] += len(t.rails)
		return
	}
	classes := x.readClasses(stem + ".kicad_pro")

	table := map[int]string{}
	tableEntry := map[string]*node{}
	for _, n := range doc.root.each("net") {
		a := n.arg(1)
		if a == nil || a.isList || a.quoted {
			continue
		}
		num, err := strconv.Atoi(a.atom)
		if err != nil {
			continue
		}
		name, _ := n.text(2)
		table[num] = name
		if name != "" && tableEntry[name] == nil {
			tableEntry[name] = n
		}
	}
	hasTable := len(table) > 0

	var fps []footprint
	netPads := map[string]int{}
	refs := map[string]int{}
	for _, fn := range doc.root.each("footprint") {
		f := footprint{fields: map[string]string{}, nets: map[string]bool{}}
		f.name, _ = fn.text(1)
		f.reference, _ = fn.property("Reference")
		f.value, _ = fn.property("Value")
		for _, t := range fn.each("fp_text") {
			switch kind, _ := t.text(1); kind {
			case "reference":
				if f.reference == "" {
					f.reference, _ = t.text(2)
				}
			case "value":
				if f.value == "" {
					f.value, _ = t.text(2)
				}
			}
		}
		if f.uuid = fn.firstText("uuid"); f.uuid == "" {
			f.uuid = fn.firstText("tstamp")
		}
		for _, seg := range strings.Split(fn.firstText("path"), "/") {
			if seg != "" {
				f.path = append(f.path, seg)
			}
		}
		for _, p := range fn.each("property") {
			name, _ := p.text(1)
			value, _ := p.text(2)
			if name == "" || notFields[name] || strings.HasPrefix(name, "ki_") || value == "" || value == "~" {
				continue
			}
			if _, seen := f.fields[name]; !seen {
				f.fields[name] = value
			}
		}
		for _, pad := range fn.each("pad") {
			net := pad.first("net")
			if net == nil {
				continue
			}
			name, cause := padNet(net, table, hasTable)
			if cause != "" {
				x.counts["pads"][cause]++
				continue
			}
			if name == "" {
				continue
			}
			x.resolved["pads"]++
			netPads[name]++
			f.nets[name] = true
		}
		f.line, f.column = doc.position(fn.start)
		f.endLine, f.endCol = doc.position(fn.end)
		f.endCol++
		refs[f.reference]++
		fps = append(fps, f)
	}

	multi, single := 0, 0
	for _, n := range netPads {
		if n >= 2 {
			multi++
		} else {
			single++
		}
	}
	boardProps := map[string]any{
		"language":        language,
		"format_version":  version,
		"parts":           len(fps),
		"nets":            len(netPads),
		"multi_pad_nets":  multi,
		"single_pad_nets": single,
		"sheet_files":     len(t.files),
	}
	if classes != nil && len(classes.names) > 0 {
		boardProps["net_classes"] = classes.names
	}
	x.out = append(x.out, facts.Fact{Kind: facts.KindModule, Name: path, File: path, Props: boardProps})
	if t.rootRead {
		x.out = append(x.out, facts.Fact{
			Kind:      facts.KindDependency,
			Name:      path + " -> " + root,
			File:      path,
			Props:     map[string]any{"language": language, "source": "internal"},
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: root}},
		})
	}

	for _, f := range fps {
		name := path + "#" + f.reference
		colliding := refs[f.reference] > 1
		switch {
		case f.reference == "" || colliding:
			if f.uuid == "" {
				x.counts["references"]["no_uuid_to_tell_apart"]++
				continue
			}
			name += "@" + f.uuid
			if f.reference == "" {
				x.counts["references"]["no_reference"]++
			} else {
				x.counts["references"]["colliding"]++
			}
		default:
			x.resolved["references"]++
		}

		props := map[string]any{
			"language":    language,
			"symbol_kind": "part",
			"reference":   f.reference,
			"footprint":   f.name,
		}
		if f.value != "" {
			props["value"] = f.value
		}
		if colliding {
			props["duplicate_reference"] = true
		}
		for k, v := range f.fields {
			props["field:"+k] = v
		}
		sheet, cause := x.place(f, t, root)
		if len(f.path) == 0 {
			props["board_only"] = true
		}
		declares := path
		if cause != "" {
			x.counts["placement"][cause]++
		} else {
			x.resolved["placement"]++
			declares = sheet
		}
		rels := []facts.Relation{{Kind: facts.RelDeclares, Target: declares}}
		nets := make([]string, 0, len(f.nets))
		for n := range f.nets {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, n := range nets {
			rels = append(rels, facts.Relation{Kind: facts.RelDependsOn, Target: path + "#net:" + n})
		}
		x.out = append(x.out, facts.Fact{
			Kind: facts.KindSymbol,
			Name: name,
			File: path,
			Line: f.line, EndLine: f.endLine, Column: f.column, EndColumn: f.endCol,
			Props:     props,
			Relations: rels,
		})
	}

	names := make([]string, 0, len(netPads))
	for n := range netPads {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		props := map[string]any{"language": language, "symbol_kind": "net", "pads": netPads[n]}
		if t.rails[n] {
			props["rail"] = true
		}
		if classes != nil {
			switch assigned := classes.assign(n); len(assigned) {
			case 0:
			case 1:
				props["net_class"] = assigned[0]
				props["net_class_defined"] = classes.defined[assigned[0]]
				if classes.defined[assigned[0]] {
					x.resolved["classes"]++
				} else {
					x.counts["classes"]["undefined_class"]++
				}
			default:
				x.counts["classes"]["claimed_by_two"]++
			}
		}
		f := facts.Fact{
			Kind:      facts.KindSymbol,
			Name:      path + "#net:" + n,
			File:      path,
			Props:     props,
			Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: path}},
		}
		if entry := tableEntry[n]; entry != nil {
			f.Line, f.Column = doc.position(entry.start)
			f.EndLine, f.EndColumn = doc.position(entry.end)
			f.EndColumn++
		}
		x.out = append(x.out, f)
	}

	for r := range t.rails {
		if netPads[r] > 0 {
			x.resolved["rails"]++
		} else {
			x.counts["rails"]["on_no_pad"]++
		}
	}
}

// padNet resolves a pad's net through the board's numbered table when it has
// one, and reads it inline when it does not. KiCad 10 writes the name quoted and
// no table; KiCad 6 to 9 write the number bare beside the name.
func padNet(net *node, table map[int]string, hasTable bool) (string, string) {
	a := net.arg(1)
	if a == nil || a.isList {
		return "", ""
	}
	if a.quoted {
		return a.atom, ""
	}
	num, err := strconv.Atoi(a.atom)
	if err != nil {
		return "", "net_unreadable"
	}
	if num == 0 {
		return "", ""
	}
	if !hasTable {
		return "", "net_not_in_table"
	}
	name, ok := table[num]
	if !ok {
		return "", "net_not_in_table"
	}
	if written, ok := net.text(2); ok && written != name {
		return "", "net_disagrees_with_table"
	}
	return name, ""
}

func (x *extraction) place(f footprint, t *tree, root string) (string, string) {
	switch {
	case len(f.path) == 0:
		return "", "no_path"
	case !x.present[root]:
		return "", "no_schematic"
	case !t.rootRead:
		return "", "sheet_not_read"
	case len(f.path) == 1:
		return root, ""
	}
	file, known := t.sheetOf[f.path[len(f.path)-2]]
	switch {
	case !known:
		return "", "path_not_in_tree"
	case file == "":
		return "", "sheet_uuid_ambiguous"
	case x.sheets[file] == nil:
		return "", "sheet_not_read"
	}
	return file, ""
}

type projectClasses struct {
	names    []string
	defined  map[string]bool
	members  map[string][]string
	patterns []classPattern
}

type classPattern struct{ class, pattern string }

func (x *extraction) readClasses(path string) *projectClasses {
	if !x.present[path] {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(x.repoPath, filepath.FromSlash(path)))
	if err != nil {
		x.counts["classes"]["project_unreadable"]++
		return nil
	}
	var project struct {
		NetSettings struct {
			Classes []struct {
				Name string   `json:"name"`
				Nets []string `json:"nets"`
			} `json:"classes"`
			Patterns []struct {
				NetClass string `json:"netclass"`
				Pattern  string `json:"pattern"`
			} `json:"netclass_patterns"`
			Assignments json.RawMessage `json:"netclass_assignments"`
		} `json:"net_settings"`
	}
	if err := json.Unmarshal(raw, &project); err != nil {
		x.counts["classes"]["project_unparsable"]++
		return nil
	}
	c := &projectClasses{defined: map[string]bool{}, members: map[string][]string{}}
	for _, class := range project.NetSettings.Classes {
		if class.Name == "" {
			continue
		}
		if !c.defined[class.Name] {
			c.defined[class.Name] = true
			c.names = append(c.names, class.Name)
		}
		for _, net := range class.Nets {
			c.members[net] = append(c.members[net], class.Name)
		}
	}
	sort.Strings(c.names)
	for _, p := range project.NetSettings.Patterns {
		if p.NetClass != "" && p.Pattern != "" {
			c.patterns = append(c.patterns, classPattern{class: p.NetClass, pattern: p.Pattern})
		}
	}
	// A per-net assignment map is a third source of classes whose shape the
	// measured projects never showed filled in, so it is counted, not read.
	if a := strings.TrimSpace(string(project.NetSettings.Assignments)); a != "" && a != "null" && a != "{}" {
		var entries map[string]json.RawMessage
		if json.Unmarshal(project.NetSettings.Assignments, &entries) == nil {
			x.counts["classes"]["assignments_unread"] += len(entries)
		} else {
			x.counts["classes"]["assignments_unread"]++
		}
	}
	return c
}

func (c *projectClasses) assign(net string) []string {
	set := map[string]bool{}
	for _, class := range c.members[net] {
		set[class] = true
	}
	for _, p := range c.patterns {
		if wildcard(p.pattern, net) {
			set[p.class] = true
		}
	}
	out := make([]string, 0, len(set))
	for class := range set {
		out = append(out, class)
	}
	sort.Strings(out)
	return out
}

// wildcard matches * and ? and nothing else, case-sensitively. KiCad's own
// matcher for class patterns was not verified; a net name can hold brackets,
// plus signs and dots, which a regular expression or a shell glob would read as
// syntax.
func wildcard(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]):
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
