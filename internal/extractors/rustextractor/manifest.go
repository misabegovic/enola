package rustextractor

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/factpath"
)

// cargoManifest is what call resolution needs from one Cargo.toml: which
// files Cargo compiles as crate roots, and which dependencies name another
// crate of this repository. Read line by line, like parseCargoPackageName,
// rather than as TOML.
type cargoManifest struct {
	dir        string
	pkg        string
	hasPackage bool
	workspace  bool
	libName    string
	libPath    string
	build      string
	buildOff   bool
	targets    []cargoTarget
	noAuto     map[string]bool
	deps       map[string]cargoDep
	wsDeps     map[string]cargoDep
	patches    map[string]cargoDep
}

type cargoTarget struct {
	kind, name, path string
}

type cargoDep struct {
	path      string
	pkg       string
	workspace bool
}

var (
	cargoKVRe      = regexp.MustCompile(`^\s*([A-Za-z0-9_\-.]+)\s*=\s*"([^"]*)"`)
	cargoFalseRe   = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=\s*false\b`)
	cargoInlineRe  = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=\s*\{(.*)\}`)
	cargoDottedRe  = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\.(path|workspace|package)\s*=\s*(.*)$`)
	cargoPathRe    = regexp.MustCompile(`\bpath\s*=\s*"([^"]*)"`)
	cargoPackageRe = regexp.MustCompile(`\bpackage\s*=\s*"([^"]*)"`)
	cargoWsRe      = regexp.MustCompile(`\bworkspace\s*=\s*true`)
	cargoKeyRe     = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=`)
)

var cargoDepSections = []string{"dependencies", "dev-dependencies", "build-dependencies"}

func parseCargoManifest(dir, data string) *cargoManifest {
	m := &cargoManifest{dir: dir, noAuto: map[string]bool{}, deps: map[string]cargoDep{},
		wsDeps: map[string]cargoDep{}, patches: map[string]cargoDep{}}
	section, depKey := "", ""
	var depTable map[string]cargoDep
	for _, line := range strings.Split(data, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") {
			s := t
			if i := strings.Index(s, "#"); i >= 0 {
				s = s[:i]
			}
			s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "[]"))
			section, depKey, depTable = s, "", nil
			switch {
			case strings.HasPrefix(t, "[[") && (s == "bin" || s == "test" || s == "example" || s == "bench"):
				m.targets = append(m.targets, cargoTarget{kind: s})
			case s == "package":
				m.hasPackage = true
			case s == "workspace" || strings.HasPrefix(s, "workspace."):
				m.workspace = true
			}
			if key, table := m.dottedDepTable(s); key != "" {
				depKey, depTable = key, table
			}
			continue
		}
		switch {
		case section == "package":
			if kv := cargoKVRe.FindStringSubmatch(t); kv != nil {
				switch kv[1] {
				case "name":
					m.pkg = kv[2]
				case "build":
					m.build = kv[2]
				}
			}
			if off := cargoFalseRe.FindStringSubmatch(t); off != nil {
				switch off[1] {
				case "build":
					m.buildOff = true
				case "autobins", "autotests", "autoexamples", "autobenches":
					m.noAuto[off[1]] = true
				}
			}
		case section == "lib":
			if kv := cargoKVRe.FindStringSubmatch(t); kv != nil {
				switch kv[1] {
				case "name":
					m.libName = kv[2]
				case "path":
					m.libPath = kv[2]
				}
			}
		case section == "bin" || section == "test" || section == "example" || section == "bench":
			if kv := cargoKVRe.FindStringSubmatch(t); kv != nil && len(m.targets) > 0 {
				target := &m.targets[len(m.targets)-1]
				switch kv[1] {
				case "name":
					target.name = kv[2]
				case "path":
					target.path = kv[2]
				}
			}
		case depKey != "":
			d := depTable[depKey]
			if kv := cargoKVRe.FindStringSubmatch(t); kv != nil {
				switch kv[1] {
				case "path":
					d.path = kv[2]
				case "package":
					d.pkg = kv[2]
				}
			}
			if cargoWsRe.MatchString(t) {
				d.workspace = true
			}
			depTable[depKey] = d
		default:
			if table := m.depTable(section); table != nil {
				readCargoDep(t, table)
			}
		}
	}
	return m
}

// depTable is the table a section's `name = ...` lines fill, or nil for a
// section that declares no dependencies. `[target.<cfg>.dependencies]` counts:
// which cfg is active is not evaluated, and a path it names is still a crate
// of this repository.
func (m *cargoManifest) depTable(section string) map[string]cargoDep {
	switch {
	case strings.HasPrefix(section, "patch."):
		return m.patches
	case section == "workspace.dependencies":
		return m.wsDeps
	}
	for _, s := range cargoDepSections {
		if section == s || (strings.HasPrefix(section, "target.") && strings.HasSuffix(section, "."+s)) {
			return m.deps
		}
	}
	return nil
}

// dottedDepTable reads a `[dependencies.name]` style header: the section holds
// one dependency's keys, one per line.
func (m *cargoManifest) dottedDepTable(section string) (string, map[string]cargoDep) {
	if strings.HasPrefix(section, "patch.") {
		if rest := strings.TrimPrefix(section, "patch."); strings.Count(rest, ".") == 1 {
			return rest[strings.Index(rest, ".")+1:], m.patches
		}
		return "", nil
	}
	if strings.HasPrefix(section, "workspace.dependencies.") {
		return strings.TrimPrefix(section, "workspace.dependencies."), m.wsDeps
	}
	for _, s := range cargoDepSections {
		if strings.HasPrefix(section, s+".") {
			return strings.TrimPrefix(section, s+"."), m.deps
		}
		if strings.HasPrefix(section, "target.") {
			if i := strings.Index(section, "."+s+"."); i >= 0 {
				return section[i+len(s)+2:], m.deps
			}
		}
	}
	return "", nil
}

func readCargoDep(line string, table map[string]cargoDep) {
	if kv := cargoInlineRe.FindStringSubmatch(line); kv != nil {
		d := table[kv[1]]
		if p := cargoPathRe.FindStringSubmatch(kv[2]); p != nil {
			d.path = p[1]
		}
		if p := cargoPackageRe.FindStringSubmatch(kv[2]); p != nil {
			d.pkg = p[1]
		}
		if cargoWsRe.MatchString(kv[2]) {
			d.workspace = true
		}
		table[kv[1]] = d
		return
	}
	if kv := cargoDottedRe.FindStringSubmatch(line); kv != nil {
		d := table[kv[1]]
		v := strings.Trim(strings.TrimSpace(kv[3]), `"`)
		switch kv[2] {
		case "path":
			d.path = v
		case "package":
			d.pkg = v
		case "workspace":
			d.workspace = v == "true"
		}
		table[kv[1]] = d
		return
	}
	if kv := cargoKeyRe.FindStringSubmatch(line); kv != nil {
		if _, ok := table[kv[1]]; !ok {
			table[kv[1]] = cargoDep{}
		}
	}
}

// rustCrate is one crate Cargo builds from this repository: its root file, and
// the names under which its code reaches the libraries of other crates here.
// Every other crate name its code writes is outside the repository.
type rustCrate struct {
	root    string
	dir     string
	lib     bool
	externs map[string]string
}

func cargoCrateName(name string) string { return strings.ReplaceAll(name, "-", "_") }

// buildRustCrates finds every crate root the way Cargo does: the library,
// each binary, integration test, example and bench, declared or discovered,
// and the build script. A dependency names a crate here when it is a path
// dependency, a workspace dependency with a path, or a registry dependency the
// workspace root patches to a path.
func buildRustCrates(repoPath string, cargoFiles []string, exists map[string]bool) map[string]*rustCrate {
	manifests := map[string]*cargoManifest{}
	var dirs []string
	for _, rel := range cargoFiles {
		data, err := os.ReadFile(filepath.Join(repoPath, rel))
		if err != nil {
			continue
		}
		d := factpath.Dir(rel)
		manifests[d] = parseCargoManifest(d, string(data))
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	join := func(d, p string) string {
		if d == "." || d == "" {
			return path.Clean(p)
		}
		return path.Clean(d + "/" + p)
	}
	workspaceOf := func(d string) *cargoManifest {
		for cur := d; ; cur = factpath.Dir(cur) {
			if m, ok := manifests[cur]; ok && m.workspace {
				return m
			}
			if cur == "." || cur == "" || cur == "/" {
				return nil
			}
		}
	}

	var files []string
	for f := range exists {
		files = append(files, f)
	}
	sort.Strings(files)

	crates := map[string]*rustCrate{}
	libOf := map[string]*rustCrate{}
	var order []*rustCrate
	for _, d := range dirs {
		m := manifests[d]
		if !m.hasPackage {
			continue
		}
		add := func(p string, lib bool) {
			p = join(d, p)
			if !exists[p] || crates[p] != nil {
				return
			}
			c := &rustCrate{root: p, dir: d, lib: lib, externs: map[string]string{}}
			crates[p] = c
			order = append(order, c)
			if lib {
				libOf[d] = c
			}
		}
		if m.libPath != "" {
			add(m.libPath, true)
		} else {
			add("src/lib.rs", true)
		}
		if !m.noAuto["autobins"] {
			add("src/main.rs", false)
		}
		for _, target := range m.targets {
			p := target.path
			if p == "" && target.name != "" {
				switch target.kind {
				case "bin":
					p = "src/bin/" + target.name + ".rs"
					if target.name == m.pkg {
						p = "src/main.rs"
					} else if !exists[join(d, p)] {
						p = "src/bin/" + target.name + "/main.rs"
					}
				case "test":
					p = "tests/" + target.name + ".rs"
				case "example":
					p = "examples/" + target.name + ".rs"
				case "bench":
					p = "benches/" + target.name + ".rs"
				}
			}
			if p != "" {
				add(p, false)
			}
		}
		switch {
		case m.buildOff:
		case m.build != "":
			add(m.build, false)
		default:
			add("build.rs", false)
		}
		prefix := d + "/"
		if d == "." {
			prefix = ""
		}
		auto := map[string]string{"tests": "autotests", "examples": "autoexamples", "benches": "autobenches"}
		for _, f := range files {
			if !strings.HasPrefix(f, prefix) {
				continue
			}
			rest := f[len(prefix):]
			parts := strings.Split(rest, "/")
			switch {
			case len(parts) == 3 && parts[0] == "src" && parts[1] == "bin" && !m.noAuto["autobins"]:
				add(rest, false)
			case len(parts) == 4 && parts[0] == "src" && parts[1] == "bin" && parts[3] == "main.rs" && !m.noAuto["autobins"]:
				add(rest, false)
			case len(parts) == 2 && auto[parts[0]] != "" && !m.noAuto[auto[parts[0]]]:
				add(rest, false)
			case len(parts) == 3 && auto[parts[0]] != "" && parts[2] == "main.rs" && !m.noAuto[auto[parts[0]]]:
				add(rest, false)
			}
		}
	}

	for _, c := range order {
		m := manifests[c.dir]
		if lib := libOf[c.dir]; lib != nil && !c.lib {
			name := m.libName
			if name == "" {
				name = m.pkg
			}
			c.externs[cargoCrateName(name)] = lib.root
		}
		keys := make([]string, 0, len(m.deps))
		for key := range m.deps {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ws := workspaceOf(c.dir)
		for _, key := range keys {
			d := m.deps[key]
			p, base := d.path, c.dir
			if p == "" && d.workspace && ws != nil {
				if wd, ok := ws.wsDeps[key]; ok && wd.path != "" {
					p, base = wd.path, ws.dir
				}
			}
			if p == "" && ws != nil {
				name := key
				if d.pkg != "" {
					name = d.pkg
				}
				if pd, ok := ws.patches[name]; ok && pd.path != "" {
					p, base = pd.path, ws.dir
				}
			}
			if p == "" {
				continue
			}
			lib := libOf[join(base, p)]
			if lib == nil {
				continue
			}
			c.externs[cargoCrateName(key)] = lib.root
			if d.pkg == "" {
				if target := manifests[lib.dir]; target != nil && target.libName != "" {
					c.externs[cargoCrateName(target.libName)] = lib.root
				}
			}
		}
	}
	return crates
}
