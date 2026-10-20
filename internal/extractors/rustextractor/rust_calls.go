package rustextractor

import (
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// A Rust call is resolved in two steps. The per-file walk records what the
// source says syntactically (the callee path, or the receiver and how its type
// is written) together with each module's declarations and imports; a
// crate-wide pass then resolves those against every file's declarations, so a
// `crate::a::f()` or a `Foo::new()` whose impl lives in another file can be
// grounded without type inference. Every step fails closed: an unknown head,
// a glob, a generic, an untyped receiver or two candidate methods leave the
// call at its bare name and count the reason.

type rustModScope struct {
	fns         map[string]bool
	types       map[string]bool
	fileMods    map[string]bool
	pathMods    map[string]bool
	inlineMods  map[string]bool
	uses        map[string][]string
	useConflict map[string]bool
	globs       [][]string
	macroItems  map[string]int
	macroTypes  map[string]bool
	// cfgItems and cfgUses name what a `#[cfg(..)]` item or `use` binds in
	// this scope: an item and a `use` of one name coexist in compiled code only
	// when cfg-exclusive or in different namespaces, and which one is live
	// depends on features the extractor does not evaluate.
	cfgItems map[string]bool
	cfgUses  map[string]bool
	// merged marks a module written as two inline bodies of one name (diesel's
	// std_compat, one body per cfg), and twins the names both bodies declare.
	// Which body is live is not evaluated, so neither declaration is the name,
	// and an item never shadows a `use` of the other body.
	merged bool
	twins  map[string]bool
	// values names the consts and statics, which a `use` imports as items;
	// pubMods the modules written plain `pub`, the only ones another crate
	// sees.
	values  map[string]bool
	pubMods map[string]bool
}

const (
	rustCallPath = iota
	rustCallSelf
	rustCallSelfField
	rustCallLocal
)

type rustPendingCall struct {
	owner, rel int
	modKey     string
	form       int
	segs       []string
	name       string
	field      string
	impl       int
	generics   map[string]bool
	boundEnv   bool
	sibling    string
}

type rustImplRec struct {
	modKey   string
	typeSegs []string
	trait    []string
	traitArg bool
	generics map[string]bool
	methods  map[string]string
	// selfRef marks an impl for a reference type; specialized marks an impl
	// for the type at a written type argument (`impl Cached<'a, Statement>`),
	// which a call on the type at another argument never reaches.
	selfRef     bool
	specialized bool
}

type rustTraitRec struct {
	modKey  string
	name    string
	methods map[string]string
	hasBody map[string]bool
}

type rustStructRec struct {
	modKey   string
	name     string
	generics map[string]bool
	fields   map[string][]string
}

type rustBinding struct {
	from, to uint
	segs     []string
}

type rustBindings struct {
	by map[string][]rustBinding
}

// rustCallRel is one calls relation the walk wrote: where it sits, and either
// why it keeps its bare leaf, or the pending call the crate-wide pass settles.
// Every calls relation has one, so the census counts each relation once.
type rustCallRel struct {
	owner   int
	rel     int
	leaf    string
	file    string
	cause   string
	pending int
	loop    bool
	scaling bool
	self    bool
}

// testRefOwner marks a relation of the file's test_ref fact, which is
// appended after the walk.
const testRefOwner = -2

// rustUseLeaf is one `use` leaf the walk wrote a dependency fact for.
type rustUseLeaf struct {
	fact   int
	segs   []string
	modKey string
	test   bool
	// testMods is how many #[cfg(test)] modules below modKey the leaf is
	// written in: the walk keys a test module's contents on its parent.
	testMods int
}

type rustFileRecs struct {
	file    string
	dir     string
	scopes  map[string]*rustModScope
	impls   []rustImplRec
	traits  []rustTraitRec
	structs []rustStructRec
	calls   []rustPendingCall
	rels    []rustCallRel
	uses    []rustUseLeaf
	testRef int
	tests   []rustTestRec
	// parameterisedTests counts the test functions whose runner names their
	// cases build.
	parameterisedTests int
}

// rustTestRec is one test function: the inline modules it sits in within its
// file, its name and its span.
type rustTestRec struct {
	segs                             []string
	name                             string
	line, endLine, column, endColumn int
}

func newRustModScope(body *sitter.Node, src []byte, parser *sitter.Parser) *rustModScope {
	s := &rustModScope{
		fns:        collectModFnNames(body, src),
		types:      map[string]bool{},
		fileMods:   map[string]bool{},
		pathMods:   map[string]bool{},
		inlineMods: map[string]bool{},
		macroItems: map[string]int{},
		macroTypes: map[string]bool{},
		cfgItems:   map[string]bool{},
		cfgUses:    map[string]bool{},
		values:     map[string]bool{},
		pubMods:    map[string]bool{},
	}
	s.readItems(body, src, parser, false, 0)
	return s
}

// readItems records what a module body declares. Inside an item macro's body
// the items get no fact (the walker does not descend into macro bodies), so
// they are counted as macro items, and the body's `use` items are recorded
// here rather than by the walker.
func (s *rustModScope) readItems(body *sitter.Node, src []byte, parser *sitter.Parser, inMacro bool, depth int) {
	if body == nil {
		return
	}
	pathAttr, cfgAttr, cfgTest := false, false, false
	for i := uint(0); i < uint(body.ChildCount()); i++ {
		c := body.Child(i)
		k := kindOf(c)
		if k == "attribute_item" {
			text := strings.Join(strings.Fields(nodeText(c, src)), "")
			if strings.Contains(text, "path") {
				pathAttr = true
			}
			if strings.HasPrefix(text, "#[cfg(") {
				cfgAttr = true
			}
			if text == "#[cfg(test)]" {
				cfgTest = true
			}
			continue
		}
		name := ""
		if n := c.ChildByFieldName("name"); n != nil {
			name = nodeText(n, src)
		}
		if cfgAttr && name != "" {
			s.cfgItems[name] = true
		}
		switch k {
		case "use_declaration":
			paths, aliases := collectUseAliases(c.ChildByFieldName("argument"), src, nil)
			for j, alias := range aliases {
				if cfgAttr {
					s.cfgUses[alias] = true
				}
				if !inMacro || cfgTest {
					continue
				}
				if alias == "*" {
					s.globs = append(s.globs, paths[j][:len(paths[j])-1])
				} else if alias != "_" {
					s.addUse(alias, paths[j])
				}
			}
		case "macro_invocation":
			s.readItemMacro(c, src, parser, depth)
		case "function_item", "function_signature_item", "const_item", "static_item":
			if inMacro && name != "" {
				s.macroItems[name]++
			} else if !inMacro && (k == "const_item" || k == "static_item") {
				s.values[name] = true
			}
		case "struct_item", "enum_item", "union_item", "type_item", "trait_item":
			if inMacro {
				s.macroItems[name]++
				if k != "trait_item" {
					s.macroTypes[name] = true
				}
			} else {
				s.types[name] = true
			}
		case "mod_item":
			if v := findChildByKind(c, "visibility_modifier"); v != nil && nodeText(v, src) == "pub" {
				s.pubMods[name] = true
			}
			if c.ChildByFieldName("body") != nil {
				if !inMacro {
					s.inlineMods[name] = true
				}
			} else {
				s.fileMods[name] = true
				if pathAttr {
					s.pathMods[name] = true
				}
			}
		}
		pathAttr, cfgAttr, cfgTest = false, false, false
	}
}

// readItemMacro reads an item-position macro (`cfg_rt! { mod blocking; }`),
// whose `mod` and `use` items are real declarations the parse tree holds only
// as tokens. The body is parsed again as items; read as text, a doc comment
// inside one (`/// ... use the Content-Type header instead. ...;`) once became
// a `use`. A body that does not parse as items (`bitflags! { struct F: u32 {
// .. } }`) is read only for its `mod x;` lines, which still name files: a name
// it appears to declare is left unknown, and an unknown name refuses.
func (s *rustModScope) readItemMacro(n *sitter.Node, src []byte, parser *sitter.Parser, depth int) {
	tree := findChildByKind(n, "token_tree")
	if tree == nil || tree.ChildCount() < 2 || depth > 4 {
		return
	}
	inner := src[tree.Child(0).EndByte():tree.Child(tree.ChildCount()-1).StartByte()]
	if parser != nil {
		parsed := parser.Parse(inner, nil)
		if parsed != nil {
			defer parsed.Close()
			if root := parsed.RootNode(); !root.HasError() {
				s.readItems(root, inner, parser, true, depth+1)
				return
			}
		}
	}
	for i := uint(0); i+2 < tree.ChildCount(); i++ {
		if nodeText(tree.Child(i), src) == "mod" && kindOf(tree.Child(i+1)) == "identifier" && nodeText(tree.Child(i+2), src) == ";" {
			s.fileMods[nodeText(tree.Child(i+1), src)] = true
		}
	}
}

func parseUseTokens(toks []string, pos *int, prefix []string) (paths [][]string, aliases []string) {
	segs := append([]string{}, prefix...)
	for *pos < len(toks) {
		t := toks[*pos]
		switch {
		case t == "::":
			*pos++
		case t == "{":
			*pos++
			for *pos < len(toks) && toks[*pos] != "}" {
				ps, as := parseUseTokens(toks, pos, segs)
				paths, aliases = append(paths, ps...), append(aliases, as...)
				if *pos < len(toks) && toks[*pos] == "," {
					*pos++
				}
			}
			*pos++
			return paths, aliases
		case t == "*":
			*pos++
			return [][]string{append(segs, "*")}, []string{"*"}
		case t == "}" || t == ",":
			goto leaf
		case t == "as":
			*pos++
			if *pos < len(toks) && len(segs) > 0 {
				alias := toks[*pos]
				*pos++
				return [][]string{segs}, []string{alias}
			}
			return nil, nil
		default:
			segs = append(segs, t)
			*pos++
		}
	}
leaf:
	if len(segs) == len(prefix) || len(segs) == 0 {
		return nil, nil
	}
	if segs[len(segs)-1] == "self" && len(segs) > 1 {
		segs = segs[:len(segs)-1]
	}
	return [][]string{segs}, []string{segs[len(segs)-1]}
}

func collectModFnNames(body *sitter.Node, src []byte) map[string]bool {
	names := map[string]bool{}
	if body == nil {
		return names
	}
	for i := uint(0); i < uint(body.ChildCount()); i++ {
		c := body.Child(i)
		switch kindOf(c) {
		case "function_item":
			if n := c.ChildByFieldName("name"); n != nil {
				names[nodeText(n, src)] = true
			}
		case "foreign_mod_item":
			for k := range collectFnNames(c.ChildByFieldName("body"), src) {
				names[k] = true
			}
		}
	}
	return names
}

// merge adds a second inline body of the same module to this scope.
func (s *rustModScope) merge(o *rustModScope) {
	s.merged = true
	if s.twins == nil {
		s.twins = map[string]bool{}
	}
	declared := func(sc *rustModScope, n string) bool {
		return sc.fns[n] || sc.types[n] || sc.fileMods[n] || sc.inlineMods[n] || sc.macroItems[n] > 0 || sc.uses[n] != nil
	}
	for _, names := range []map[string]bool{o.fns, o.types, o.fileMods, o.inlineMods} {
		for n := range names {
			if declared(s, n) {
				s.twins[n] = true
			}
		}
	}
	for n := range o.macroItems {
		if declared(s, n) {
			s.twins[n] = true
		}
	}
	for n := range o.uses {
		if declared(s, n) {
			s.twins[n] = true
		}
	}
	union := func(into, from map[string]bool) {
		for k, v := range from {
			if v {
				into[k] = true
			}
		}
	}
	union(s.fns, o.fns)
	union(s.types, o.types)
	union(s.fileMods, o.fileMods)
	union(s.pathMods, o.pathMods)
	union(s.inlineMods, o.inlineMods)
	union(s.macroTypes, o.macroTypes)
	union(s.cfgItems, o.cfgItems)
	union(s.cfgUses, o.cfgUses)
	union(s.values, o.values)
	union(s.pubMods, o.pubMods)
	for k, n := range o.macroItems {
		s.macroItems[k] += n
	}
	for k, segs := range o.uses {
		s.addUse(k, segs)
	}
	s.globs = append(s.globs, o.globs...)
}

func (s *rustModScope) addUse(alias string, segs []string) {
	if s.uses == nil {
		s.uses = map[string][]string{}
		s.useConflict = map[string]bool{}
	}
	if prev, ok := s.uses[alias]; ok && strings.Join(prev, "::") != strings.Join(segs, "::") {
		s.useConflict[alias] = true
	}
	s.uses[alias] = segs
}

// collectUseAliases is collectUseItems keeping the name each leaf is bound
// to: an `as` alias when present, the leaf otherwise, the parent segment for
// a `self` leaf.
func collectUseAliases(node *sitter.Node, src []byte, prefix []string) (paths [][]string, aliases []string) {
	if node == nil {
		return nil, nil
	}
	switch kindOf(node) {
	case "use_as_clause":
		p := node.ChildByFieldName("path")
		a := node.ChildByFieldName("alias")
		if p == nil || a == nil {
			return nil, nil
		}
		ps, _ := collectUseAliases(p, src, prefix)
		for range ps {
			aliases = append(aliases, nodeText(a, src))
		}
		return ps, aliases
	case "scoped_use_list":
		newPrefix := prefix
		if p := node.ChildByFieldName("path"); p != nil {
			newPrefix = appendSegs(prefix, strings.Split(nodeText(p, src), "::")...)
		}
		if l := node.ChildByFieldName("list"); l != nil {
			return collectUseAliases(l, src, newPrefix)
		}
		return nil, nil
	case "use_list":
		for i := uint(0); i < uint(node.ChildCount()); i++ {
			if c := node.Child(i); c.IsNamed() {
				ps, as := collectUseAliases(c, src, prefix)
				paths = append(paths, ps...)
				aliases = append(aliases, as...)
			}
		}
		return paths, aliases
	case "use_wildcard":
		segs := append([]string{}, prefix...)
		for i := uint(0); i < uint(node.ChildCount()); i++ {
			if c := node.Child(i); c.IsNamed() {
				segs = appendSegs(prefix, strings.Split(nodeText(c, src), "::")...)
				break
			}
		}
		return [][]string{append(segs, "*")}, []string{"*"}
	}
	for _, segs := range collectUseItems(node, src, prefix) {
		last := segs[len(segs)-1]
		if last == "self" && len(segs) > 1 {
			segs = segs[:len(segs)-1]
			last = segs[len(segs)-1]
		}
		paths = append(paths, segs)
		aliases = append(aliases, last)
	}
	return paths, aliases
}

// rustTypeSegs reads a written type as a path when it names one nominal type
// directly or behind a shared or mutable reference. Anything else (a tuple, a
// slice, a trait object, `impl Trait`, a pointer) is no single type to look
// methods up on.
func rustTypeSegs(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	switch kindOf(n) {
	case "reference_type":
		return rustTypeSegs(n.ChildByFieldName("type"), src)
	case "type_identifier":
		return []string{nodeText(n, src)}
	case "generic_type":
		return rustTypeSegs(n.ChildByFieldName("type"), src)
	case "scoped_type_identifier", "scoped_identifier":
		t := nodeText(n, src)
		if strings.ContainsAny(t, "<> ") {
			return nil
		}
		return strings.Split(t, "::")
	case "identifier":
		return []string{nodeText(n, src)}
	}
	return nil
}

// rustSpecialized reports a self type written with a type argument that is
// not one of the impl's own parameters: `Cached<'a, Statement>` is, and
// `Cached<'a, T>` for `impl<'a, T>` is not.
func rustSpecialized(n *sitter.Node, generics map[string]bool, src []byte) bool {
	for n != nil && kindOf(n) == "reference_type" {
		n = n.ChildByFieldName("type")
	}
	if n == nil || kindOf(n) != "generic_type" {
		return false
	}
	args := n.ChildByFieldName("type_arguments")
	if args == nil {
		return false
	}
	for i := uint(0); i < uint(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch kindOf(a) {
		case "lifetime":
			continue
		case "type_identifier":
			if generics[nodeText(a, src)] {
				continue
			}
		}
		return true
	}
	return false
}

func rustGenericNames(n *sitter.Node, src []byte, into map[string]bool) map[string]bool {
	if into == nil {
		into = map[string]bool{}
	}
	if n == nil {
		return into
	}
	for i := uint(0); i < uint(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if kindOf(c) == "type_parameter" {
			if nm := c.ChildByFieldName("name"); nm != nil {
				into[nodeText(nm, src)] = true
			}
		}
	}
	return into
}

// collectBindings counts every name a function binds, in its parameters and
// anywhere in its body, and keeps a written type only for a name bound exactly
// once with that type in view. A name bound twice (shadowing, a closure
// parameter, a match arm) keeps no type, so no scope analysis is needed to
// stay correct: the resolver refuses it instead.
func collectBindings(params, body *sitter.Node, src []byte) *rustBindings {
	b := &rustBindings{by: map[string][]rustBinding{}}
	simple := func(p *sitter.Node) string {
		if p == nil {
			return ""
		}
		if kindOf(p) == "mut_pattern" {
			for i := uint(0); i < uint(p.NamedChildCount()); i++ {
				if c := p.NamedChild(i); kindOf(c) == "identifier" {
					return nodeText(c, src)
				}
			}
			return ""
		}
		if kindOf(p) == "identifier" {
			return nodeText(p, src)
		}
		return ""
	}
	const open = ^uint(0)
	bind := func(name string, from, to uint, segs []string) {
		b.by[name] = append(b.by[name], rustBinding{from: from, to: to, segs: segs})
	}
	var countIdents func(n *sitter.Node, from, to uint)
	countIdents = func(n *sitter.Node, from, to uint) {
		if n == nil {
			return
		}
		switch kindOf(n) {
		case "identifier", "shorthand_field_identifier":
			bind(nodeText(n, src), from, to, nil)
			return
		}
		for i := uint(0); i < uint(n.ChildCount()); i++ {
			countIdents(n.Child(i), from, to)
		}
	}
	// A binding lives until the end of the block, arm or closure that holds
	// it. An enclosing node the walk does not recognise leaves it open to the
	// end of the function, which only ever refuses more.
	blockEnd := func(n *sitter.Node) uint {
		for p := n.Parent(); p != nil; p = p.Parent() {
			switch kindOf(p) {
			case "block", "match_arm", "closure_expression":
				return p.EndByte()
			case "function_item":
				return open
			}
		}
		return open
	}
	if params != nil {
		for i := uint(0); i < uint(params.NamedChildCount()); i++ {
			p := params.NamedChild(i)
			if kindOf(p) != "parameter" {
				continue
			}
			pat := p.ChildByFieldName("pattern")
			if name := simple(pat); name != "" {
				bind(name, 0, open, rustTypeSegs(p.ChildByFieldName("type"), src))
			} else {
				countIdents(pat, 0, open)
			}
		}
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch kindOf(n) {
		case "function_item", "token_tree":
			return
		case "let_declaration":
			pat := n.ChildByFieldName("pattern")
			if name := simple(pat); name != "" {
				var segs []string
				if t := n.ChildByFieldName("type"); t != nil {
					segs = rustTypeSegs(t, src)
				} else if v := n.ChildByFieldName("value"); v != nil && kindOf(v) == "struct_expression" {
					segs = rustTypeSegs(v.ChildByFieldName("name"), src)
				}
				bind(name, n.EndByte(), blockEnd(n), segs)
			} else {
				countIdents(pat, n.EndByte(), blockEnd(n))
			}
		case "for_expression":
			from, to := n.EndByte(), n.EndByte()
			if body := n.ChildByFieldName("body"); body != nil {
				from = body.StartByte()
			}
			countIdents(n.ChildByFieldName("pattern"), from, to)
		case "let_condition":
			// if-let and while-let bindings reach the consequence and, for a let
			// chain, later conditions; the enclosing expression bounds both.
			to := open
			if p := n.Parent(); p != nil {
				for q := p; q != nil; q = q.Parent() {
					if k := kindOf(q); k == "if_expression" || k == "while_expression" {
						to = q.EndByte()
						break
					}
				}
			}
			countIdents(n.ChildByFieldName("pattern"), n.EndByte(), to)
		case "closure_parameters":
			to := open
			if p := n.Parent(); p != nil {
				to = p.EndByte()
			}
			countIdents(n, n.EndByte(), to)
		case "match_pattern":
			cond := n.ChildByFieldName("condition")
			for i := uint(0); i < uint(n.ChildCount()); i++ {
				c := n.Child(i)
				if cond != nil && c.StartByte() == cond.StartByte() && c.EndByte() == cond.EndByte() {
					continue
				}
				to := open
				if p := n.Parent(); p != nil {
					to = p.EndByte()
				}
				countIdents(c, n.StartByte(), to)
			}
		}
		for i := uint(0); i < uint(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(body)
	return b
}

// typeOf answers for a use at byte offset at: the name must have exactly one
// binding that can be in scope there, and that binding must carry a type.
// A binding's scope end is not tracked, so a binding in an earlier sibling
// block still counts, which only ever refuses more.
func (b *rustBindings) typeOf(name string, at uint) ([]string, bool) {
	if b == nil {
		return nil, false
	}
	var hit *rustBinding
	for i, x := range b.by[name] {
		if x.from <= at && at < x.to {
			if hit != nil {
				return nil, false
			}
			hit = &b.by[name][i]
		}
	}
	if hit == nil || hit.segs == nil {
		return nil, false
	}
	return hit.segs, true
}

func (b *rustBindings) binds(name string, at uint) bool {
	if b == nil {
		return false
	}
	for _, x := range b.by[name] {
		if x.from <= at && at < x.to {
			return true
		}
	}
	return false
}

// ---- the crate-wide pass ----------------------------------------------------

type rustMod struct{ file, key string }

const (
	entNone = iota
	entMod
	entFn
	entType
	entMethod
	entExternal
	entUnknown
	entValue
)

type rustEnt struct {
	kind   int
	mod    rustMod
	fact   string
	typeID string
	name   string
	cause  string
	// absent is a path whose last module was found and holds nothing of the
	// name in the namespace asked.
	absent bool
}

type rustResolver struct {
	files     map[string]*rustFileRecs
	byDir     map[string][]string
	crates    map[string]*rustCrate
	parent    map[string]*rustMod
	outside   map[string]bool
	visiting  map[string]bool
	ambig     map[string]bool
	implsOf   map[string][]*rustImplRec
	implFile  map[*rustImplRec]string
	implType  map[*rustImplRec]string
	structs   map[string]*rustStructRec
	structAt  map[*rustStructRec]string
	traits    map[string]*rustTraitRec
	traitAt   map[*rustTraitRec]string
	blanket   map[string]string
	nameFiles map[string]map[string]bool
	// memo keeps every answer lookup has given. The scopes and crates are
	// complete before the first lookup, parentFile, the one lazy cache it
	// reaches, is a function of them that never asks lookup back, and the
	// depth is in the key because a shallower walk sees further before the
	// cap, so a remembered answer is the one a fresh walk would give. Modules
	// that glob each other (diesel's) are then walked once per depth instead
	// of once per path reaching them.
	memo map[rustLookupKey]rustEnt
}

type rustLookupKey struct {
	file, key, name string
	ns, depth       int
}

func typeID(file, key, name string) string { return file + "#" + key + "#" + name }

func joinKey(key, name string) string {
	if key == "" {
		return name
	}
	return key + "." + name
}

func (r *rustResolver) scope(m rustMod) *rustModScope {
	f := r.files[m.file]
	if f == nil {
		return nil
	}
	return f.scopes[m.key]
}

// ownsDir reports whether a file's `mod x;` children live in its own
// directory: a crate root and a mod.rs do, any other module file names a
// directory after itself.
func (r *rustResolver) ownsDir(file string) bool {
	return path.Base(file) == "mod.rs" || r.crates[file] != nil
}

func (r *rustResolver) declares(m rustMod, child string) bool {
	s := r.scope(m)
	return s != nil && s.fileMods[child] && !s.pathMods[child]
}

// parentFile finds the one file whose `mod x;` declares this file's module.
// A crate root has none. A file nothing declares is outside every module
// tree Cargo builds (a trybuild fixture, a file left behind), and two
// declarers make its place in the tree unknowable here; everything that needs
// the place refuses in both cases.
func (r *rustResolver) parentFile(file string) (*rustMod, bool) {
	if r.crates[file] != nil {
		return nil, true
	}
	if r.ambig[file] || r.outside[file] {
		return nil, false
	}
	if p, ok := r.parent[file]; ok {
		return p, true
	}
	if r.visiting[file] {
		return nil, false
	}
	r.visiting[file] = true
	defer delete(r.visiting, file)

	d := path.Dir(file)
	name, where := strings.TrimSuffix(path.Base(file), ".rs"), d
	if path.Base(file) == "mod.rs" {
		name, where = path.Base(d), path.Dir(d)
	}
	var found []rustMod
	for _, cand := range r.byDir[where] {
		if cand != file && r.ownsDir(cand) && r.declares(rustMod{cand, ""}, name) {
			found = append(found, rustMod{cand, ""})
		}
	}
	if where != "." && where != "" {
		sib := where + ".rs"
		if r.files[sib] != nil && !r.ownsDir(sib) && r.declares(rustMod{sib, ""}, name) {
			found = append(found, rustMod{sib, ""})
		}
	}
	switch len(found) {
	case 0:
		r.outside[file] = true
		return nil, false
	case 1:
		p := found[0]
		r.parent[file] = &p
		return &p, true
	}
	r.ambig[file] = true
	return nil, false
}

func (r *rustResolver) childDir(file string) string {
	d := path.Dir(file)
	if r.ownsDir(file) {
		return d
	}
	stem := strings.TrimSuffix(path.Base(file), ".rs")
	if d == "." {
		return stem
	}
	return d + "/" + stem
}

func (r *rustResolver) child(m rustMod, name string) (rustMod, bool) {
	s := r.scope(m)
	if s == nil {
		return rustMod{}, false
	}
	if s.inlineMods[name] {
		return rustMod{m.file, joinKey(m.key, name)}, true
	}
	if m.key != "" || !s.fileMods[name] || s.pathMods[name] {
		return rustMod{}, false
	}
	cd := r.childDir(m.file)
	var hits []string
	for _, c := range []string{cd + "/" + name + ".rs", cd + "/" + name + "/mod.rs"} {
		c = strings.TrimPrefix(c, "./")
		if r.files[c] != nil {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		return rustMod{}, false
	}
	return rustMod{hits[0], ""}, true
}

func (r *rustResolver) parentMod(m rustMod) (rustMod, bool) {
	if m.key != "" {
		if i := strings.LastIndex(m.key, "."); i >= 0 {
			return rustMod{m.file, m.key[:i]}, true
		}
		return rustMod{m.file, ""}, true
	}
	p, ok := r.parentFile(m.file)
	if !ok || p == nil {
		return rustMod{}, false
	}
	return *p, true
}

// root walks a file up to the crate root that compiles it, or names why it
// cannot: the file is outside every module tree, or its place is ambiguous.
func (r *rustResolver) root(file string) (rustMod, string) {
	cur := file
	for i := 0; i < 64; i++ {
		if r.crates[cur] != nil {
			return rustMod{cur, ""}, ""
		}
		p, ok := r.parentFile(cur)
		if !ok {
			if r.outside[cur] {
				return rustMod{}, "outside_module_tree"
			}
			return rustMod{}, "crate_root_unknown"
		}
		if p == nil {
			return rustMod{cur, ""}, ""
		}
		cur = p.file
	}
	return rustMod{}, "crate_root_unknown"
}

// externLib is the library root a crate name reaches from this file's crate,
// when that library is in this repository.
func (r *rustResolver) externLib(file, name string) (rustMod, bool) {
	root, cause := r.root(file)
	if cause != "" {
		return rustMod{}, false
	}
	lib, ok := r.crates[root.file].externs[name]
	if !ok || r.files[lib] == nil {
		return rustMod{}, false
	}
	return rustMod{lib, ""}, true
}

func (r *rustResolver) fnFact(m rustMod, name string) string {
	return r.files[m.file].dir + "." + joinKey(m.key, name)
}

const (
	nsValue = iota
	nsType
)

func (r *rustResolver) lookup(m rustMod, name string, ns, depth int) rustEnt {
	k := rustLookupKey{m.file, m.key, name, ns, depth}
	if e, ok := r.memo[k]; ok {
		return e
	}
	e := r.lookupUncached(m, name, ns, depth)
	if r.memo == nil {
		r.memo = map[rustLookupKey]rustEnt{}
	}
	r.memo[k] = e
	return e
}

func (r *rustResolver) lookupUncached(m rustMod, name string, ns, depth int) rustEnt {
	s := r.scope(m)
	if s == nil {
		return rustEnt{kind: entUnknown, cause: "module_unknown"}
	}
	// An item declared inside an item macro (`cfg_x! { struct T; }`) may be
	// one cfg alternative of several. Declared once, with nothing else in the
	// module under that name, it is the module's item; declared again, in
	// another macro or beside an import of the same name, no single
	// declaration is the name.
	if s.twins[name] || (s.merged && s.uses[name] != nil && (s.fns[name] || s.types[name])) {
		return rustEnt{kind: entUnknown, cause: "merged_module_twin"}
	}
	if n := s.macroItems[name]; n > 0 {
		_, imported := s.uses[name]
		if n > 1 || imported || s.fns[name] || s.types[name] {
			return rustEnt{kind: entUnknown, cause: "cfg_macro_item"}
		}
		if ns == nsType && s.macroTypes[name] {
			return rustEnt{kind: entType, typeID: typeID(m.file, m.key, name)}
		}
		// A function an item macro declares leaves the type namespace to a
		// module of the name: tracing-subscriber's `registry` is both.
		if c, ok := r.child(m, name); ok && ns == nsType {
			return rustEnt{kind: entMod, mod: c}
		}
		return rustEnt{kind: entUnknown, mod: m, cause: "macro_item"}
	}
	// A module beside a `use` of its name is left out: axum's `pub mod serve`
	// sits beside `pub use self::serve::serve`, a module and a function, both
	// live under one cfg.
	if _, imported := s.uses[name]; imported && (s.cfgItems[name] || s.cfgUses[name]) &&
		((ns == nsValue && s.fns[name]) || (ns == nsType && s.types[name])) {
		return rustEnt{kind: entUnknown, cause: "cfg_item_import_twin"}
	}
	if ns == nsValue && s.fns[name] {
		return rustEnt{kind: entFn, mod: m, fact: r.fnFact(m, name)}
	}
	if ns == nsValue && s.values[name] {
		return rustEnt{kind: entValue, mod: m, fact: r.fnFact(m, name)}
	}
	if ns == nsType {
		if s.types[name] {
			return rustEnt{kind: entType, typeID: typeID(m.file, m.key, name)}
		}
		if c, ok := r.child(m, name); ok {
			return rustEnt{kind: entMod, mod: c}
		}
		if s.fileMods[name] || s.inlineMods[name] {
			return rustEnt{kind: entUnknown, cause: "module_file_unknown"}
		}
	}
	viaUse := false
	if segs, ok := s.uses[name]; ok {
		if s.useConflict[name] {
			return rustEnt{kind: entUnknown, cause: "import_conflict"}
		}
		e := r.resolvePath(m, segs, ns, depth+1)
		// A use shadows a glob only in the namespaces it binds: diesel's dsl
		// imports the function `select` beside a glob that holds the type.
		if !e.absent || len(s.globs) == 0 {
			return e
		}
		viaUse = true
	}
	// A glob of a module in this repository is looked into; a name two globs
	// supply would not compile, so one hit is the answer. A glob of anything
	// else could supply any name, so a miss there is unknown, not absent.
	external := false
	var hits []rustEnt
	for _, g := range s.globs {
		if depth > 6 {
			external = true
			break
		}
		t := r.resolvePath(m, g, nsType, depth+1)
		if t.kind != entMod && t.kind != entType {
			external = true
			continue
		}
		if t.kind == entType {
			continue
		}
		if e := r.lookup(t.mod, name, ns, depth+1); e.kind != entNone {
			if e.kind == entUnknown {
				external = true
				continue
			}
			hits = append(hits, e)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	if len(hits) > 1 {
		return rustEnt{kind: entUnknown, cause: "glob_ambiguous"}
	}
	if external && viaUse {
		return rustEnt{kind: entUnknown, cause: "use_beside_glob"}
	}
	if external {
		return rustEnt{kind: entUnknown, cause: "glob_import"}
	}
	if viaUse {
		return rustEnt{kind: entUnknown, cause: "path_unresolved", absent: true}
	}
	return rustEnt{kind: entNone}
}

func (r *rustResolver) resolvePath(ctx rustMod, segs []string, lastNS, depth int) rustEnt {
	if depth > 8 || len(segs) == 0 {
		return rustEnt{kind: entUnknown, cause: "path_too_deep"}
	}
	var cur rustEnt
	i := 0
	switch segs[0] {
	case "crate":
		m, cause := r.root(ctx.file)
		if cause != "" {
			return rustEnt{kind: entUnknown, cause: cause}
		}
		cur, i = rustEnt{kind: entMod, mod: m}, 1
	case "self":
		cur, i = rustEnt{kind: entMod, mod: ctx}, 1
	case "super":
		m := ctx
		for i < len(segs) && segs[i] == "super" {
			p, ok := r.parentMod(m)
			if !ok {
				return rustEnt{kind: entUnknown, cause: "super_unknown"}
			}
			m, i = p, i+1
		}
		cur = rustEnt{kind: entMod, mod: m}
	default:
		ns := lastNS
		if len(segs) > 1 {
			ns = nsType
		}
		cur = r.lookup(ctx, segs[0], ns, depth)
		if cur.kind == entNone && len(segs) == 1 {
			return cur
		}
		// A workspace crate named at the head of a path is reachable through the
		// extern prelude even beside a glob of another crate: that glob would
		// have to export a module under the same name.
		if cur.kind == entUnknown && cur.cause == "glob_import" && len(segs) > 1 {
			if _, ok := r.externLib(ctx.file, segs[0]); ok {
				cur = rustEnt{kind: entNone}
			}
		}
		if cur.kind == entNone {
			if m, ok := r.externLib(ctx.file, segs[0]); ok && len(segs) > 1 {
				cur = rustEnt{kind: entMod, mod: m}
			} else if rustStdlibCrates[segs[0]] {
				return rustEnt{kind: entExternal, cause: "std"}
			} else {
				return rustEnt{kind: entExternal, cause: "external_or_unknown_head"}
			}
		}
		i = 1
	}
	for ; i < len(segs); i++ {
		ns := nsType
		if i == len(segs)-1 {
			ns = lastNS
		}
		switch cur.kind {
		case entMod:
			cur = r.lookup(cur.mod, segs[i], ns, depth)
			if cur.kind == entNone {
				return rustEnt{kind: entUnknown, cause: "path_unresolved", absent: i == len(segs)-1}
			}
		case entType:
			if i == len(segs)-1 && lastNS == nsValue {
				return rustEnt{kind: entMethod, typeID: cur.typeID, name: segs[i]}
			}
			return rustEnt{kind: entUnknown, cause: "associated_type_path"}
		default:
			return cur
		}
	}
	if cur.kind == entMod && lastNS == nsValue {
		return rustEnt{kind: entUnknown, cause: "path_names_module"}
	}
	return cur
}

func (r *rustResolver) typeOf(ctx rustMod, segs []string, generics map[string]bool, selfType string) (string, string) {
	if len(segs) == 0 {
		return "", "receiver_untyped"
	}
	if segs[0] == "Self" && len(segs) == 1 {
		if selfType == "" {
			return "", "self_type_unknown"
		}
		return selfType, ""
	}
	if generics[segs[0]] {
		return "", "generic"
	}
	e := r.resolvePath(ctx, segs, nsType, 0)
	switch e.kind {
	case entType:
		return e.typeID, ""
	case entExternal:
		return "", "type_outside_repo"
	}
	if e.cause != "" {
		return "", e.cause
	}
	return "", "type_unresolved"
}

func (r *rustResolver) implTypeOf(file string, rec *rustImplRec) string {
	if t, ok := r.implType[rec]; ok {
		return t
	}
	id, _ := r.typeOf(rustMod{file, rec.modKey}, rec.typeSegs, rec.generics, "")
	r.implType[rec] = id
	return id
}

// methodOf returns the one method a type's impl blocks declare under name,
// counting blocks rather than fact names, so two impls that share a name (two
// `impl From<X> for T`, cfg twins, an inherent and a trait method) are a tie,
// and a tie is refused rather than broken. A path call `T::f()` takes inherent
// methods only: rust-analyzer names the trait's declaration for a trait
// method reached by path, and so does rustc's own path resolution. A method
// call takes a trait-impl method only when the trait takes no type argument
// and no generic bound is in scope, since either can select a different impl
// or the bound's own method; and it never takes a method of an impl for the
// type at a written type argument, since the receiver's argument is not read. A path call `T::f()` writes no
// argument, so the one inherent `f` is the one rustc infers the argument for.
func (r *rustResolver) methodOf(tid, name string, byPath, boundEnv bool) (fact, file, cause string) {
	type candidate struct {
		fact, file                        string
		trait, traitArg, generic, special bool
	}
	var inherent, viaTrait []candidate
	seenDefault := map[string]bool{}
	for _, rec := range r.implsOf[tid] {
		if f, ok := rec.methods[name]; ok {
			c := candidate{fact: f, file: r.implFile[rec], trait: len(rec.trait) > 0, traitArg: rec.traitArg,
				generic: len(rec.generics) > 0, special: rec.specialized}
			if c.trait {
				viaTrait = append(viaTrait, c)
			} else {
				inherent = append(inherent, c)
			}
			continue
		}
		if len(rec.trait) == 0 {
			continue
		}
		tr, _ := r.typeOf(rustMod{r.implFile[rec], rec.modKey}, rec.trait, rec.generics, "")
		if t := r.traits[tr]; t != nil && t.hasBody[name] && !seenDefault[tr] {
			seenDefault[tr] = true
			viaTrait = append(viaTrait, candidate{fact: t.methods[name], file: r.traitAt[t], trait: true,
				traitArg: rec.traitArg, generic: len(rec.generics) > 0, special: rec.specialized})
		}
	}
	if cause := r.blanket[name]; cause != "" {
		return "", "", cause
	}
	var one candidate
	if byPath {
		switch {
		case len(inherent) > 1:
			return "", "", "ambiguous_impl"
		case len(inherent) == 0 && len(viaTrait) > 0:
			return "", "", "trait_method_by_path"
		case len(inherent) == 0:
			return "", "", "no_method_on_type"
		}
		one = inherent[0]
	} else {
		switch n := len(inherent) + len(viaTrait); {
		case n == 0:
			return "", "", "no_method_on_type"
		case n > 1:
			return "", "", "ambiguous_impl"
		case len(inherent) == 1:
			one = inherent[0]
		default:
			one = viaTrait[0]
		}
	}
	switch {
	case one.special && !byPath:
		return "", "", "impl_needs_type_args"
	case one.trait && one.traitArg:
		return "", "", "trait_impl_needs_type_args"
	case one.trait && (one.generic || boundEnv):
		return "", "", "trait_method_under_bound"
	}
	return one.fact, one.file, ""
}

// rustResolution is what the crate-wide pass adds to the per-file walk: the
// census of calls and of `use` leaves, and one dependency fact per shared-name
// call target by file.
type rustResolution struct {
	callsResolved, importsResolved int
	callCauses, importCauses       map[string]int
	facts                          []facts.Fact

	testCases  []facts.Fact
	testCauses map[string]int
}

func resolveRustCrate(allFacts []facts.Fact, recs []*rustFileRecs, bases []int, crates map[string]*rustCrate, moduleDirs map[string]bool) rustResolution {
	r := &rustResolver{
		files: map[string]*rustFileRecs{}, byDir: map[string][]string{}, crates: crates,
		parent: map[string]*rustMod{}, outside: map[string]bool{}, visiting: map[string]bool{}, ambig: map[string]bool{},
		implsOf: map[string][]*rustImplRec{}, implFile: map[*rustImplRec]string{}, implType: map[*rustImplRec]string{},
		structs: map[string]*rustStructRec{}, structAt: map[*rustStructRec]string{},
		traits: map[string]*rustTraitRec{}, traitAt: map[*rustTraitRec]string{}, blanket: map[string]string{}, nameFiles: map[string]map[string]bool{},
	}
	for _, f := range recs {
		if f == nil {
			continue
		}
		r.files[f.file] = f
		d := path.Dir(f.file)
		r.byDir[d] = append(r.byDir[d], f.file)
	}
	for d := range r.byDir {
		sort.Strings(r.byDir[d])
	}
	for i := range allFacts {
		if allFacts[i].Kind != facts.KindSymbol {
			continue
		}
		n := allFacts[i].Name
		if r.nameFiles[n] == nil {
			r.nameFiles[n] = map[string]bool{}
		}
		r.nameFiles[n][allFacts[i].File] = true
	}
	for _, f := range recs {
		if f == nil {
			continue
		}
		for i := range f.traits {
			t := &f.traits[i]
			r.traits[typeID(f.file, t.modKey, t.name)] = t
			r.traitAt[t] = f.file
		}
		for i := range f.structs {
			s := &f.structs[i]
			r.structs[typeID(f.file, s.modKey, s.name)] = s
			r.structAt[s] = f.file
		}
		for i := range f.impls {
			r.implFile[&f.impls[i]] = f.file
		}
	}
	for _, f := range recs {
		if f == nil {
			continue
		}
		for i := range f.impls {
			rec := &f.impls[i]
			if len(rec.typeSegs) == 1 && rec.generics[rec.typeSegs[0]] {
				cause := "blanket_impl"
				if rec.selfRef {
					cause = "blanket_impl_on_reference"
				}
				for m := range rec.methods {
					if r.blanket[m] != "blanket_impl_on_reference" {
						r.blanket[m] = cause
					}
				}
				continue
			}
			if tid := r.implTypeOf(f.file, rec); tid != "" {
				r.implsOf[tid] = append(r.implsOf[tid], rec)
			}
		}
	}

	for fi, f := range recs {
		if f == nil {
			continue
		}
		for k := range f.rels {
			rec := &f.rels[k]
			if rec.pending < 0 {
				continue
			}
			c := f.calls[rec.pending]
			target, file, cause := r.resolveOne(f, c)
			if cause != "" {
				rec.cause = cause
				continue
			}
			rec.file = file
			rel := &allFacts[bases[fi]+rec.owner].Relations[rec.rel]
			rel.Target = target
			owner := &allFacts[bases[fi]+rec.owner]
			// A name several files declare cannot say which of them the call
			// reached, so only a name the caller's file alone declares is
			// recursion.
			rec.self = target == owner.Name && len(r.nameFiles[target]) == 1
		}
	}
	var out rustResolution
	out.callsResolved, out.callCauses, out.facts = settleRustCalls(allFacts, recs, bases, r.nameFiles)
	out.importsResolved, out.importCauses = r.resolveImports(allFacts, recs, bases, moduleDirs)
	out.testCases, out.testCauses = r.testCases(recs)
	return out
}

// testCases names each test function the way cargo test lists it: the module
// path from its target's crate root, through the file's own inline modules, to
// the function. A file whose place in a module tree is unknown gives its tests
// no name, and they are counted under the cause the resolver gives.
func (r *rustResolver) testCases(recs []*rustFileRecs) ([]facts.Fact, map[string]int) {
	var out []facts.Fact
	causes := map[string]int{}
	for _, f := range recs {
		if f == nil {
			continue
		}
		if f.parameterisedTests > 0 {
			causes["parameterised_name"] += f.parameterisedTests
		}
		if len(f.tests) == 0 {
			continue
		}
		filePath, cause := r.modulePath(f.file)
		if cause != "" {
			causes[cause] += len(f.tests)
			continue
		}
		for _, t := range f.tests {
			groups := append(append([]string(nil), filePath...), t.segs...)
			runner := strings.Join(append(append([]string(nil), groups...), t.name), "::")
			out = append(out, facts.NewTestCase("rust", f.file, groups, t.name, runner, t.line, t.endLine, t.column, t.endColumn))
		}
	}
	return out, causes
}

// modulePath is the chain of module names from the crate root that compiles a
// file down to the file: empty for a root, a file's stem, or its directory's
// name for a mod.rs.
func (r *rustResolver) modulePath(file string) ([]string, string) {
	var rev []string
	for cur, depth := file, 0; r.crates[cur] == nil; depth++ {
		p, ok := r.parentFile(cur)
		switch {
		case depth == 64 || !ok && !r.outside[cur]:
			return nil, "crate_root_unknown"
		case !ok:
			return nil, "outside_module_tree"
		case p == nil:
			return reversed(rev), ""
		}
		name := strings.TrimSuffix(path.Base(cur), ".rs")
		if path.Base(cur) == "mod.rs" {
			name = path.Base(path.Dir(cur))
		}
		rev = append(rev, name)
		cur = p.file
	}
	return reversed(rev), ""
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// settleRustCalls finishes what the pass started, on final targets. A
// qualified target whose fact name several files declare cannot be told apart
// by the string every consumer binds, so it goes back to its bare leaf. The
// loop call lists and recursion are recorded again from the targets that
// stand, test_ref holds each target once, and every calls relation is counted
// once, resolved or with one cause.
//
// A call the resolver grounded on one of several declarations sharing a name
// keeps its bare leaf, and the file it reached is kept on a dependency fact
// from the caller's file, one calls relation carrying target_file: the shape
// the constraints evaluator already narrows by file, and one no symbol
// rollup reads. A refused call is counted on the fact that made it, as
// unresolved_calls.
func settleRustCalls(allFacts []facts.Fact, recs []*rustFileRecs, bases []int, nameFiles map[string]map[string]bool) (int, map[string]int, []facts.Fact) {
	resolved := 0
	causes := map[string]int{}
	var shared []facts.Fact
	for fi, f := range recs {
		if f == nil {
			continue
		}
		ownerIdx := func(rec rustCallRel) int {
			if rec.owner == testRefOwner {
				return bases[fi] + f.testRef
			}
			return bases[fi] + rec.owner
		}
		factOf := func(rec rustCallRel) *facts.Fact { return &allFacts[ownerIdx(rec)] }
		located := map[int]map[string]int{}
		var locatedOrder []int
		sharedSeen := map[[2]string]bool{}
		type loopLists struct{ in, scaling []string }
		loops := map[int]*loopLists{}
		var loopOrder []int
		recursive := map[int]bool{}
		dropped := map[*facts.Fact]map[int]bool{}
		seenTestRef := map[string]bool{}
		for k := range f.rels {
			rec := &f.rels[k]
			fact := factOf(*rec)
			rel := &fact.Relations[rec.rel]
			if strings.Contains(rel.Target, ".") {
				file := rec.file
				if file == "" {
					file = f.file
				}
				switch {
				case !nameFiles[rel.Target][file]:
					// The name the pass built is not declared in the file it
					// chose: a function nested in a method body is named after
					// the method's receiver, not the module. Linked by the name
					// it would reach another file's fact, or none.
					rel.Target = rec.leaf
					rec.cause = "target_without_fact"
				case len(nameFiles[rel.Target]) == 1:
					rec.cause = ""
				default:
					if key := [2]string{rel.Target, file}; rec.owner != testRefOwner && !sharedSeen[key] {
						sharedSeen[key] = true
						shared = append(shared, facts.Fact{
							Kind: facts.KindDependency,
							Name: facts.CallByFileName(f.file, rel.Target, file),
							File: f.file,
							Line: fact.Line, EndLine: fact.EndLine, Column: fact.Column, EndColumn: fact.EndColumn,
							Props: map[string]any{
								"language":           "rust",
								"source":             "internal",
								facts.PropTargetFile: file,
							},
							Relations: []facts.Relation{{Kind: facts.RelCalls, Target: rel.Target}},
						})
					}
					rel.Target = rec.leaf
					rec.cause = "shared_name"
				}
			} else if rec.cause == "" {
				rec.cause = "unclassified"
			}
			if rec.owner == testRefOwner {
				if seenTestRef[rel.Target] {
					if dropped[fact] == nil {
						dropped[fact] = map[int]bool{}
					}
					dropped[fact][rec.rel] = true
					continue
				}
				seenTestRef[rel.Target] = true
			}
			if rec.cause == "" {
				resolved++
			} else {
				causes[rec.cause]++
				idx := ownerIdx(*rec)
				if located[idx] == nil {
					located[idx] = map[string]int{}
					locatedOrder = append(locatedOrder, idx)
				}
				located[idx][rec.cause]++
			}
			if rec.owner == testRefOwner {
				continue
			}
			if rec.self {
				recursive[rec.owner] = true
			}
			if rec.loop {
				l := loops[rec.owner]
				if l == nil {
					l = &loopLists{in: []string{}, scaling: []string{}}
					loops[rec.owner] = l
					loopOrder = append(loopOrder, rec.owner)
				}
				if !slices.Contains(l.in, rel.Target) {
					l.in = append(l.in, rel.Target)
				}
				if rec.scaling && !slices.Contains(l.scaling, rel.Target) {
					l.scaling = append(l.scaling, rel.Target)
				}
			}
		}
		for _, owner := range loopOrder {
			fact := &allFacts[bases[fi]+owner]
			if _, has := fact.Prop("calls_in_loop"); has {
				fact.SetProp("calls_in_loop", loops[owner].in)
				fact.SetProp("calls_in_scaling_loop", loops[owner].scaling)
			}
		}
		for owner := range recursive {
			allFacts[bases[fi]+owner].SetProp("recursive_self", true)
		}
		for _, idx := range locatedOrder {
			allFacts[idx].SetProp("unresolved_calls", causeCounts(located[idx]))
		}
		for fact, rels := range dropped {
			kept := fact.Relations[:0]
			for i, rel := range fact.Relations {
				if !rels[i] {
					kept = append(kept, rel)
				}
			}
			fact.Relations = kept
		}
	}
	return resolved, causes, shared
}

// causeCounts writes counts as sorted `cause=count` pairs, the format
// extcoverage writes for unresolved_macros.
func causeCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+strconv.Itoa(counts[name]))
	}
	return strings.Join(pairs, ",")
}

func (r *rustResolver) selfTypeOf(f *rustFileRecs, c rustPendingCall) string {
	if c.impl < 0 || c.impl >= len(f.impls) {
		return ""
	}
	return r.implTypeOf(f.file, &f.impls[c.impl])
}

// siblingInTraitImpl grounds `self.m()` or `Self::m()` written inside a trait
// impl whose block declares m. The block's own method is the answer only when
// the type is written in this repository and nothing else on the type carries
// the name: an inherent m wins over the trait's in Rust, and a type from
// another crate may have one that is not visible here.
func (r *rustResolver) siblingInTraitImpl(f *rustFileRecs, c rustPendingCall) (fact, file, cause string) {
	tid := r.selfTypeOf(f, c)
	if tid == "" {
		return "", "", "self_type_outside_repo"
	}
	var inherent []string
	var inherentAt string
	others := 0
	own := &f.impls[c.impl]
	for _, rec := range r.implsOf[tid] {
		m, ok := rec.methods[c.name]
		if !ok {
			continue
		}
		if len(rec.trait) == 0 {
			inherent = append(inherent, m)
			inherentAt = r.implFile[rec]
		} else if rec != own {
			others++
		}
	}
	if cause := r.blanket[c.name]; cause != "" {
		return "", "", cause
	}
	if c.form == rustCallPath && len(inherent) == 1 && others == 0 {
		return inherent[0], inherentAt, ""
	}
	if len(inherent) > 0 || others > 0 {
		return "", "", "ambiguous_impl"
	}
	return c.sibling, f.file, ""
}

func (r *rustResolver) resolveOne(f *rustFileRecs, c rustPendingCall) (target, file, cause string) {
	ctx := rustMod{f.file, c.modKey}
	if _, cause := r.root(f.file); cause == "outside_module_tree" {
		return "", "", cause
	}
	if c.sibling != "" {
		return r.siblingInTraitImpl(f, c)
	}
	var tid string
	switch c.form {
	case rustCallPath:
		if len(c.segs) > 0 && c.segs[0] == "Self" && len(c.segs) == 1 {
			if tid = r.selfTypeOf(f, c); tid == "" {
				return "", "", "self_type_unknown"
			}
			break
		}
		if len(c.segs) > 0 && c.generics[c.segs[0]] {
			return "", "", "generic"
		}
		e := r.resolvePath(ctx, append(append([]string{}, c.segs...), c.name), nsValue, 0)
		switch e.kind {
		case entFn:
			return e.fact, e.mod.file, ""
		case entMethod:
			tid = e.typeID
		case entExternal:
			return "", "", "path_outside_repo"
		case entNone:
			return "", "", "not_in_scope"
		default:
			if e.cause == "" {
				return "", "", "path_unresolved"
			}
			return "", "", e.cause
		}
	case rustCallSelf:
		if tid = r.selfTypeOf(f, c); tid == "" {
			return "", "", "self_type_unknown"
		}
	case rustCallSelfField:
		st := r.selfTypeOf(f, c)
		s := r.structs[st]
		if s == nil {
			return "", "", "self_type_unknown"
		}
		segs, ok := s.fields[c.field]
		if !ok || segs == nil {
			return "", "", "field_untyped"
		}
		tid, cause = r.typeOf(rustMod{r.structAt[s], s.modKey}, segs, s.generics, st)
		if tid == "" {
			return "", "", cause
		}
	case rustCallLocal:
		tid, cause = r.typeOf(ctx, c.segs, c.generics, r.selfTypeOf(f, c))
		if tid == "" {
			return "", "", cause
		}
	}
	return r.methodOf(tid, c.name, c.form == rustCallPath, c.boundEnv)
}

// ---- walker side -----------------------------------------------------------

func (w *astWalker) modKey() string { return strings.Join(w.modStack, ".") }

func (w *astWalker) bindings() *rustBindings {
	if len(w.bindStack) == 0 {
		return nil
	}
	return w.bindStack[len(w.bindStack)-1]
}

func (w *astWalker) generics() map[string]bool {
	out := map[string]bool{}
	for _, g := range w.genStack {
		for k := range g {
			out[k] = true
		}
	}
	return out
}

func (w *astWalker) recordStruct(node *sitter.Node, name string) {
	if w.inTestMod || len(w.typeStack) > 0 {
		return
	}
	rec := rustStructRec{
		modKey:   w.modKey(),
		name:     name,
		generics: rustGenericNames(node.ChildByFieldName("type_parameters"), w.src, nil),
		fields:   map[string][]string{},
	}
	if body := node.ChildByFieldName("body"); body != nil {
		switch kindOf(body) {
		case "field_declaration_list":
			for i := uint(0); i < uint(body.NamedChildCount()); i++ {
				c := body.NamedChild(i)
				if kindOf(c) != "field_declaration" {
					continue
				}
				if n := c.ChildByFieldName("name"); n != nil {
					rec.fields[nodeText(n, w.src)] = rustTypeSegs(c.ChildByFieldName("type"), w.src)
				}
			}
		case "ordered_field_declaration_list":
			idx := 0
			for i := uint(0); i < uint(body.NamedChildCount()); i++ {
				c := body.NamedChild(i)
				if k := kindOf(c); k == "attribute_item" || k == "visibility_modifier" {
					continue
				}
				rec.fields[strconv.Itoa(idx)] = rustTypeSegs(c, w.src)
				idx++
			}
		}
	}
	w.recs.structs = append(w.recs.structs, rec)
}

func (w *astWalker) recordTrait(body *sitter.Node, name string) {
	if w.inTestMod || body == nil {
		return
	}
	rec := rustTraitRec{modKey: w.modKey(), name: name, methods: map[string]string{}, hasBody: map[string]bool{}}
	for i := uint(0); i < uint(body.ChildCount()); i++ {
		c := body.Child(i)
		k := kindOf(c)
		if k != "function_item" && k != "function_signature_item" {
			continue
		}
		if n := c.ChildByFieldName("name"); n != nil {
			m := nodeText(n, w.src)
			rec.methods[m] = w.dir + "." + w.qualify(m)
			if k == "function_item" {
				rec.hasBody[m] = true
			}
		}
	}
	w.recs.traits = append(w.recs.traits, rec)
}

func (w *astWalker) recordImpl(node, body *sitter.Node) int {
	if w.inTestMod {
		return -1
	}
	typeNode := node.ChildByFieldName("type")
	rec := rustImplRec{
		modKey:   w.modKey(),
		typeSegs: rustTypeSegs(typeNode, w.src),
		generics: rustGenericNames(node.ChildByFieldName("type_parameters"), w.src, nil),
		methods:  map[string]string{},
		selfRef:  kindOf(typeNode) == "reference_type",
	}
	rec.specialized = rustSpecialized(typeNode, rec.generics, w.src)
	if t := node.ChildByFieldName("trait"); t != nil {
		if rec.trait = rustTypeSegs(t, w.src); rec.trait == nil {
			rec.trait = []string{"?"}
		}
		rec.traitArg = kindOf(t) == "generic_type"
	}
	if body != nil {
		for i := uint(0); i < uint(body.ChildCount()); i++ {
			c := body.Child(i)
			if kindOf(c) != "function_item" {
				continue
			}
			if n := c.ChildByFieldName("name"); n != nil {
				m := nodeText(n, w.src)
				rec.methods[m] = w.dir + "." + w.qualify(m)
			}
		}
	}
	w.recs.impls = append(w.recs.impls, rec)
	return len(w.recs.impls) - 1
}

// emitCall writes a calls relation and its record. A pending call keeps its
// bare leaf until the crate-wide pass grounds it; any other bare leaf carries
// the cause the walk already knows; a qualified target is the walk's own
// answer, checked for a shared name once every file is merged.
func (w *astWalker) emitCall(target, cause string, p *rustPendingCall) {
	if w.inTestMod {
		if p != nil && cause == "" {
			cause = "test_code"
		}
		w.testRefCall(target, cause)
		return
	}
	w.recordCallMetrics(target)
	idx := w.fileRefIdx
	if len(w.ownerStack) > 0 {
		idx = w.ownerStack[len(w.ownerStack)-1]
	} else {
		idx = w.ensureFileRefFact()
	}
	w.out[idx].Relations = append(w.out[idx].Relations, facts.Relation{Kind: facts.RelCalls, Target: target})
	rec := rustCallRel{owner: idx, rel: len(w.out[idx].Relations) - 1, leaf: rustLeaf(target), cause: cause, pending: -1,
		loop: w.loopDepth > 0, scaling: w.scalingDepth > 0, self: w.fnSelfName != "" && target == w.fnSelfName}
	if p != nil && cause == "" {
		p.owner, p.rel, p.name = rec.owner, rec.rel, target
		p.modKey = w.modKey()
		p.impl = -1
		if len(w.implStack) > 0 {
			p.impl = w.implStack[len(w.implStack)-1]
		}
		p.generics = w.generics()
		p.boundEnv = len(p.generics) > 0 || w.whereDepth > 0
		w.recs.calls = append(w.recs.calls, *p)
		rec.pending = len(w.recs.calls) - 1
	}
	w.recs.rels = append(w.recs.rels, rec)
}

// testRefCall writes one relation of the file's test_ref fact, which holds
// each target once. Test code is not resolved across files, so a bare leaf
// there keeps its cause or counts as test code.
func (w *astWalker) testRefCall(target, cause string) {
	if w.testRefSeen == nil {
		w.testRefSeen = make(map[string]bool)
	}
	if w.testRefSeen[target] {
		return
	}
	w.testRefSeen[target] = true
	w.testRefRels = append(w.testRefRels, facts.Relation{Kind: facts.RelCalls, Target: target})
	if cause == "" && !strings.Contains(target, ".") {
		cause = "test_code"
	}
	w.recs.rels = append(w.recs.rels, rustCallRel{owner: testRefOwner, rel: len(w.testRefRels) - 1, leaf: rustLeaf(target), cause: cause, pending: -1})
}

// rustLeaf is the name a call is written with: a qualified target's last
// segment, which the walk only ever builds from the written name.
func rustLeaf(target string) string {
	return target[strings.LastIndex(target, ".")+1:]
}

// emitBareCall resolves `f()`. A local binding of that name shadows every
// item, and a bare call never reaches a method, so neither is guessed; a name
// the file neither declares nor imports is left for the crate-wide pass, which
// refuses it rather than assuming a sibling file's function.
func (w *astWalker) emitBareCall(name string, at uint) {
	if w.bindings().binds(name, at) {
		w.emitCall(name, "local_binding", nil)
		return
	}
	if w.isKnownFn(name) {
		w.emitCall(w.dir+"."+w.qualifyMod(name), "", nil)
		return
	}
	if target, ok := w.importMap[name]; ok && target == "" {
		return
	}
	if rustBuiltins[name] {
		return
	}
	w.emitCall(name, "", &rustPendingCall{form: rustCallPath})
}

// pendingFor reads how a call's receiver or path is written, or names why it
// cannot be read.
func (w *astWalker) pendingFor(fn *sitter.Node, name string) (*rustPendingCall, string) {
	if fn != nil && kindOf(fn) == "generic_function" {
		fn = fn.ChildByFieldName("function")
	}
	if fn == nil {
		return nil, "callee_form"
	}
	switch kindOf(fn) {
	case "field_expression":
		v := fn.ChildByFieldName("value")
		switch kindOf(v) {
		case "self":
			if w.selfWrappedFor(name) {
				return nil, "self_wrapped"
			}
			return &rustPendingCall{form: rustCallSelf}, ""
		case "identifier":
			segs, ok := w.bindings().typeOf(nodeText(v, w.src), v.StartByte())
			if !ok {
				return nil, "receiver_untyped"
			}
			return &rustPendingCall{form: rustCallLocal, segs: segs}, ""
		case "field_expression":
			if inner := v.ChildByFieldName("value"); inner != nil && kindOf(inner) == "self" {
				if f := v.ChildByFieldName("field"); f != nil {
					return &rustPendingCall{form: rustCallSelfField, field: nodeText(f, w.src)}, ""
				}
			}
		}
		return nil, "receiver_expression"
	case "scoped_identifier":
		segs := rustPathSegs(fn.ChildByFieldName("path"), w.src)
		if segs == nil {
			return nil, "path_form"
		}
		return &rustPendingCall{form: rustCallPath, segs: segs}, ""
	}
	return nil, "callee_form"
}

func rustPathSegs(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	switch kindOf(n) {
	case "identifier", "self", "super", "crate", "type_identifier":
		return []string{nodeText(n, src)}
	case "generic_type":
		return rustPathSegs(n.ChildByFieldName("type"), src)
	case "scoped_identifier", "scoped_type_identifier":
		p, nm := n.ChildByFieldName("path"), n.ChildByFieldName("name")
		if p == nil || nm == nil {
			return nil
		}
		ps := rustPathSegs(p, src)
		if ps == nil {
			return nil
		}
		return append(ps, nodeText(nm, src))
	}
	return nil
}

func tokenIs(n *sitter.Node, kinds ...string) bool {
	if n == nil {
		return false
	}
	k := kindOf(n)
	for _, want := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// tokenTreeReceiver reads `recv.name(` inside a macro's token tree, where the
// receiver is only tokens: `self`, `self.field` or a single local name.
func (w *astWalker) tokenTreeReceiver(tree *sitter.Node, i uint, name string) (*rustPendingCall, string) {
	at := func(j int) *sitter.Node {
		if j < 0 {
			return nil
		}
		return tree.Child(uint(j))
	}
	k := int(i)
	recv := at(k - 2)
	if recv == nil {
		return nil, "receiver_expression"
	}
	before := at(k - 3)
	isSelf := func(n *sitter.Node) bool { return n != nil && nodeText(n, w.src) == "self" }
	switch {
	case isSelf(recv) && !tokenIs(before, ".", "::"):
		if w.selfWrappedFor(name) {
			return nil, "self_wrapped"
		}
		return &rustPendingCall{form: rustCallSelf}, ""
	case kindOf(recv) == "identifier" && tokenIs(before, ".") && isSelf(at(k-4)) && !tokenIs(at(k-5), ".", "::"):
		return &rustPendingCall{form: rustCallSelfField, field: nodeText(recv, w.src)}, ""
	case kindOf(recv) == "identifier" && !tokenIs(before, ".", "::"):
		segs, ok := w.bindings().typeOf(nodeText(recv, w.src), recv.StartByte())
		if !ok {
			return nil, "receiver_untyped"
		}
		return &rustPendingCall{form: rustCallLocal, segs: segs}, ""
	}
	return nil, "receiver_expression"
}

// tokenTreePath reads `a::b::name(` inside a macro's token tree. Without the
// path, `tempfile::tempdir()` would read as a bare call to a local `tempdir`.
func (w *astWalker) tokenTreePath(tree *sitter.Node, i uint, name string) (*rustPendingCall, string) {
	var segs []string
	j := int(i) - 1
	for j >= 1 && kindOf(tree.Child(uint(j))) == "::" {
		p := tree.Child(uint(j - 1))
		if !tokenIs(p, "identifier", "self", "super", "crate") {
			return nil, "path_form"
		}
		segs = append([]string{nodeText(p, w.src)}, segs...)
		j -= 2
	}
	if j >= 0 && tokenIs(tree.Child(uint(j)), "::", ".", ">", "$") {
		return nil, "path_form"
	}
	if len(segs) == 0 {
		return nil, "path_form"
	}
	return &rustPendingCall{form: rustCallPath, segs: segs}, ""
}

// rustWrapperMethods are the methods a `self: Pin<&mut Self>`, `Box<Self>`,
// `Rc<Self>` or `Arc<Self>` receiver finds on the wrapper before reaching
// Self: the wrappers' inherent methods and the std traits they implement.
var rustWrapperMethods = map[string]bool{
	"as_mut": true, "as_ref": true, "get_mut": true, "get_ref": true, "get_unchecked_mut": true,
	"set": true, "into_ref": true, "into_inner": true, "map_unchecked": true, "map_unchecked_mut": true,
	"deref": true, "deref_mut": true, "clone": true, "fmt": true, "eq": true, "ne": true, "cmp": true,
	"partial_cmp": true, "hash": true, "to_owned": true, "to_string": true, "into": true, "try_into": true,
	"borrow": true, "borrow_mut": true, "type_id": true, "as_deref": true, "as_deref_mut": true,
	"downcast": true, "downcast_ref": true, "downcast_mut": true, "is": true,
}

func (w *astWalker) selfWrappedFor(name string) bool {
	return len(w.selfWrapStack) > 0 && w.selfWrapStack[len(w.selfWrapStack)-1] && rustWrapperMethods[name]
}

// selfParamWrapped reports a `self: Pin<&mut Self>`-style receiver, where a
// method call on self probes the wrapper's own methods first.
func selfParamWrapped(params *sitter.Node, src []byte) bool {
	if params == nil {
		return false
	}
	for i := uint(0); i < uint(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if kindOf(p) != "parameter" {
			continue
		}
		pat := p.ChildByFieldName("pattern")
		if pat == nil || kindOf(pat) != "self" {
			continue
		}
		t := strings.Join(strings.Fields(nodeText(p.ChildByFieldName("type"), src)), "")
		switch t {
		case "Self", "&Self", "&mutSelf":
			return false
		}
		return true
	}
	return false
}

// rustExprMacros take expressions as input and evaluate them as written, so a
// receiver or a path inside their arguments means what it says. A macro of
// any other name may rebind `self`, wrap a path or never evaluate it.
var rustExprMacros = map[string]bool{
	"format": true, "format_args": true, "print": true, "println": true, "eprint": true, "eprintln": true,
	"write": true, "writeln": true, "panic": true, "assert": true, "assert_eq": true, "assert_ne": true,
	"debug_assert": true, "debug_assert_eq": true, "debug_assert_ne": true, "vec": true, "dbg": true,
	"unreachable": true, "todo": true, "unimplemented": true,
}

func (w *astWalker) inExprMacro(tree *sitter.Node) bool {
	n := tree
	for n != nil && kindOf(n) == "token_tree" {
		n = n.Parent()
	}
	if n == nil || kindOf(n) != "macro_invocation" {
		return false
	}
	m := n.ChildByFieldName("macro")
	if m == nil {
		return false
	}
	name := nodeText(m, w.src)
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	return rustExprMacros[name]
}

// emitSelfSibling handles a sibling call inside an impl block. In an inherent
// impl the block's own method is the answer, as before; in a trait impl it is
// checked against the rest of the type's impls by the crate-wide pass.
func (w *astWalker) emitSelfSibling(fn *sitter.Node, name string) {
	target := w.dir + "." + w.qualify(name)
	top := -1
	if len(w.implStack) > 0 {
		top = w.implStack[len(w.implStack)-1]
	}
	if w.implTrait == "" || top < 0 {
		w.emitCall(target, "", nil)
		return
	}
	p := &rustPendingCall{form: rustCallSelf, sibling: target}
	if kindOf(fn) == "scoped_identifier" {
		p.form = rustCallPath
	}
	w.emitCall(name, "", p)
}
