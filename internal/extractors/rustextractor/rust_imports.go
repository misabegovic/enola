package rustextractor

import (
	"path"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// A `use` leaf lands on what it names. An item with a fact lands on that fact
// and carries the file it is declared in (target_file), since an item's fact
// name is `<dir>.<ident>` and several files of one directory may declare the
// same one. A module lands on the directory holding its file and carries no
// file: a module fact's file is its directory, and a carried file would stop
// the constraints evaluator binding the module by its name. An item with no
// fact (one declared inside an item macro) lands on its file's directory. A
// leaf binding both a module and an item that the importer can see is a
// namespace twin, and lands as the module. External and std imports keep
// their target, and a leaf the resolver refuses keeps the walk's, with the
// crate root Cargo names in place of the manifest directory.

type rustImport struct {
	target, file, cause string
	external            bool
}

func (r *rustResolver) resolveImports(allFacts []facts.Fact, recs []*rustFileRecs, bases []int, moduleDirs map[string]bool) (int, map[string]int) {
	resolved := 0
	causes := map[string]int{}
	for fi, f := range recs {
		if f == nil {
			continue
		}
		for _, u := range f.uses {
			dep := &allFacts[bases[fi]+u.fact]
			imp := r.resolveUse(f, u, dep.PropString("source"))
			if imp.external {
				continue
			}
			rel := &dep.Relations[0]
			switch {
			case imp.target != "":
				rel.Target = imp.target
			default:
				if fallback := r.fallbackImport(f, u, moduleDirs); fallback != "" {
					rel.Target = fallback
				}
			}
			if imp.file != "" {
				dep.SetProp(facts.PropTargetFile, imp.file)
			}
			if imp.cause == "" {
				resolved++
			} else {
				causes[imp.cause]++
			}
		}
	}
	return resolved, causes
}

func (r *rustResolver) resolveUse(f *rustFileRecs, u rustUseLeaf, source string) rustImport {
	segs := u.segs
	if n := len(segs); n > 1 && segs[n-1] == "self" {
		segs = segs[:n-1]
	}
	if u.test {
		// A use inside a #[cfg(test)] module is relative to that module, whose
		// scope the walk does not record, so it is counted and not resolved.
		if source != "internal" {
			return rustImport{external: true}
		}
		return rustImport{cause: "test_code"}
	}
	ctx := rustMod{f.file, u.modKey}
	if _, cause := r.root(f.file); cause == "outside_module_tree" {
		if source != "internal" {
			return rustImport{external: true}
		}
		return rustImport{cause: cause}
	}
	if n := len(segs); n > 0 && segs[n-1] == "*" {
		if n == 1 {
			return rustImport{cause: "glob_import"}
		}
		if e := r.resolvePath(ctx, segs[:n-1], nsType, 0); e.kind == entExternal {
			return rustImport{external: true}
		}
		return rustImport{cause: "glob_import"}
	}
	if len(segs) == 0 {
		return rustImport{cause: "path_form"}
	}
	asType := r.resolvePath(ctx, segs, nsType, 0)
	asValue := r.resolvePath(ctx, segs, nsValue, 0)
	if asType.kind == entExternal || asValue.kind == entExternal {
		return rustImport{external: true}
	}
	item, itemOK := r.importedItem(asType, asValue)
	if asType.kind == entMod {
		if itemOK && r.moduleVisible(asType.mod, f.file) {
			return rustImport{target: moduleDir(asType.mod.file), cause: "namespace_twin"}
		}
		if !itemOK {
			return rustImport{target: moduleDir(asType.mod.file)}
		}
	}
	// A module declared where no file is found for it (a cfg_attr path) is
	// still a module the leaf may bind beside the item.
	if itemOK && asType.cause == "module_file_unknown" {
		return rustImport{cause: "namespace_twin"}
	}
	if itemOK {
		return item
	}
	for _, e := range []rustEnt{asValue, asType} {
		if e.cause != "" {
			return rustImport{cause: e.cause}
		}
	}
	return rustImport{cause: "path_unresolved"}
}

// importedItem reads the item a leaf names in either namespace: a function,
// const or static in the value namespace, a type or trait in the type
// namespace, or an item an item macro declares. A leaf naming a type and a
// value that are two declarations (diesel's `dsl::select`, a type alias
// beside a function) binds both, so neither is the item it imports; so does
// a leaf whose type namespace a glob the pass cannot read may fill beside a
// use that binds only the value.
func (r *rustResolver) importedItem(asType, asValue rustEnt) (rustImport, bool) {
	var fact, file string
	switch {
	case (asValue.kind == entFn || asValue.kind == entValue) && (asType.kind == entType || asType.cause == "use_beside_glob"):
		return rustImport{cause: "type_value_twin"}, true
	case asValue.kind == entFn || asValue.kind == entValue:
		fact, file = asValue.fact, asValue.mod.file
	case asType.kind == entType:
		parts := strings.SplitN(asType.typeID, "#", 3)
		if len(parts) != 3 || r.files[parts[0]] == nil {
			return rustImport{}, false
		}
		file = parts[0]
		fact = r.files[file].dir + "." + joinKey(parts[1], parts[2])
	case asValue.kind == entUnknown && asValue.cause == "macro_item" && asValue.mod.file != "":
		return rustImport{target: moduleDir(asValue.mod.file), cause: "item_without_fact"}, true
	default:
		return rustImport{}, false
	}
	if !r.nameFiles[fact][file] {
		return rustImport{target: moduleDir(file), cause: "item_without_fact"}, true
	}
	return rustImport{target: fact, file: file}, true
}

// moduleVisible reports whether a module can be named from file: always
// inside its own crate, and from another crate only when written plain `pub`.
func (r *rustResolver) moduleVisible(m rustMod, from string) bool {
	own, fromCrate := r.crateOf(m.file), r.crateOf(from)
	if own == "" || fromCrate == "" || own == fromCrate {
		return true
	}
	if m.key != "" {
		parentKey, name := "", m.key
		if i := strings.LastIndex(m.key, "."); i >= 0 {
			parentKey, name = m.key[:i], m.key[i+1:]
		}
		s := r.scope(rustMod{m.file, parentKey})
		return s != nil && s.pubMods[name]
	}
	p, ok := r.parentFile(m.file)
	if !ok || p == nil {
		return true
	}
	name := strings.TrimSuffix(path.Base(m.file), ".rs")
	if path.Base(m.file) == "mod.rs" {
		name = path.Base(path.Dir(m.file))
	}
	s := r.scope(*p)
	return s != nil && s.pubMods[name]
}

func (r *rustResolver) crateOf(file string) string {
	root, cause := r.root(file)
	if cause != "" {
		return ""
	}
	return root.file
}

// fallbackImport is the walk's target for a leaf the resolver does not place,
// read from the module tree rather than from directories. A crate:: leaf
// starts at the crate root Cargo names: a manifest directory holding only
// build.rs beside src/ was once taken for the source root, and every crate::
// import it could not place landed on ".". A file no crate root reaches (one a
// #[path] attribute includes) starts at the nearest crate root above it. A
// self:: or super:: leaf starts at the module it is written in, an inline
// test module included: the walk took super for the parent directory, which
// from src/main.rs is ".".
func (r *rustResolver) fallbackImport(f *rustFileRecs, u rustUseLeaf, moduleDirs map[string]bool) string {
	segs := u.segs
	if n := len(segs); n > 0 && (segs[n-1] == "*" || segs[n-1] == "self") {
		segs = segs[:n-1]
	}
	if len(segs) == 0 {
		return ""
	}
	switch segs[0] {
	case "crate":
		if root, cause := r.root(f.file); cause == "" {
			return joinRustPath(path.Dir(root.file), segs[1:], moduleDirs)
		}
		if root := r.nearestCrateRoot(f.file); root != "" {
			return joinRustPath(path.Dir(root), segs[1:], moduleDirs)
		}
		return ""
	case "self", "super":
		m, i, below := rustMod{f.file, u.modKey}, 0, u.testMods
		if segs[0] == "self" {
			i = 1
		}
		for ; i < len(segs) && segs[i] == "super"; i++ {
			if below > 0 {
				below--
				continue
			}
			p, ok := r.parentMod(m)
			if !ok {
				return ""
			}
			m = p
		}
		return joinRustPath(moduleDir(m.file), segs[i:], moduleDirs)
	}
	if lib, ok := r.externLib(f.file, segs[0]); ok {
		return joinRustPath(path.Dir(lib.file), segs[1:], moduleDirs)
	}
	return ""
}

// nearestCrateRoot is the crate root whose directory holds file most closely,
// the longer directory winning and the smaller name breaking a tie.
func (r *rustResolver) nearestCrateRoot(file string) string {
	dir := path.Dir(file)
	best := ""
	for root := range r.crates {
		d := path.Dir(root)
		if d != "." && d != dir && !strings.HasPrefix(dir, d+"/") {
			continue
		}
		if best == "" || len(d) > len(path.Dir(best)) || (len(d) == len(path.Dir(best)) && root < best) {
			best = root
		}
	}
	return best
}

// moduleDir is the directory holding a module's file, the module fact a
// module leaf lands on.
func moduleDir(file string) string {
	return path.Dir(file)
}
