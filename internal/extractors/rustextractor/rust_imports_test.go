package rustextractor

import (
	"strconv"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// dependencyNamed finds the dependency fact a `use` leaf wrote, by its
// `<dir> -> <path>` name.
func dependencyNamed(t *testing.T, ff []facts.Fact, name string) facts.Fact {
	t.Helper()
	for _, f := range ff {
		if f.Kind == facts.KindDependency && f.Name == name {
			return f
		}
	}
	var names []string
	for _, f := range ff {
		if f.Kind == facts.KindDependency {
			names = append(names, f.Name)
		}
	}
	t.Fatalf("no dependency fact %q among %v", name, names)
	return facts.Fact{}
}

func requireImport(t *testing.T, dep facts.Fact, target, targetFile string) {
	t.Helper()
	if len(dep.Relations) != 1 || dep.Relations[0].Kind != facts.RelImports || dep.Relations[0].Target != target {
		t.Errorf("%s: want imports -> %s, got %+v", dep.Name, target, dep.Relations)
	}
	if got := dep.PropString(facts.PropTargetFile); got != targetFile {
		t.Errorf("%s: want target_file %q, got %q", dep.Name, targetFile, got)
	}
}

func importsCensus(t *testing.T, ff []facts.Fact) (detected, resolved int, causes map[string]int) {
	t.Helper()
	for _, f := range ff {
		if f.Kind != facts.KindExtraction || f.Name != "rust:imports" {
			continue
		}
		coverage, _ := f.PropAny("edge_coverage").([]map[string]any)
		if len(coverage) != 1 {
			t.Fatalf("rust:imports carries one edge_coverage entry, got %v", f.PropAny("edge_coverage"))
		}
		detected, _ = coverage[0]["detected"].(int)
		resolved, _ = coverage[0]["resolved"].(int)
		causes = map[string]int{}
		if list := f.PropString("unresolved_macros"); list != "" {
			for _, pair := range strings.Split(list, ",") {
				name, count, _ := strings.Cut(pair, "=")
				n, err := strconv.Atoi(count)
				if err != nil {
					t.Fatalf("cause %q: %v", pair, err)
				}
				causes[name] = n
			}
		}
		return detected, resolved, causes
	}
	t.Fatalf("no rust:imports extraction fact")
	return 0, 0, nil
}

var coreFiles = map[string]string{
	"Cargo.toml":       demoCrate,
	"src/lib.rs":       "mod core;\nmod hooks;\nmod util;\npub use crate::core::count;\n",
	"src/core/mod.rs":  "pub fn count() -> u32 { 1 }\npub fn other() -> u32 { 2 }\npub struct Tally;\n",
	"src/util.rs":      "pub fn helper() {}\n",
	"src/hooks/mod.rs": "",
}

func withHooks(hooks string) map[string]string {
	files := map[string]string{"src/hooks/mod.rs": hooks}
	for k, v := range coreFiles {
		if _, set := files[k]; !set {
			files[k] = v
		}
	}
	return files
}

func TestExtract_ItemImportLandsOnTheItemWithItsFile(t *testing.T) {
	ff := extractRustRepo(t, withHooks("use crate::core::count;\nuse crate::core::Tally;\npub fn run() -> u32 { count() }\n"))
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::count"), "src/core.count", "src/core/mod.rs")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::Tally"), "src/core.Tally", "src/core/mod.rs")
}

// A module fact's file is its directory, so a carried file would make the
// constraints evaluator refuse the module's own exact name: a module leaf
// lands on the directory holding the module's file and carries none.
func TestExtract_ModuleImportLandsOnItsDirectoryWithoutAFile(t *testing.T) {
	ff := extractRustRepo(t, withHooks("use crate::core;\nuse crate::util;\nuse crate::core::{self};\n"))
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core"), "src/core", "")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::util"), "src", "")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::self"), "src/core", "")
}

func TestExtract_AliasGroupAndReexportChainLandOnTheItem(t *testing.T) {
	ff := extractRustRepo(t, withHooks("use crate::core::{count as tally, other};\nuse crate::count;\n"))
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::count"), "src/core.count", "src/core/mod.rs")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::other"), "src/core.other", "src/core/mod.rs")
	// `use crate::count` reaches the item through lib.rs's `pub use`.
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::count"), "src/core.count", "src/core/mod.rs")
}

func TestExtract_ExternalImportKeepsItsTarget(t *testing.T) {
	ff := extractRustRepo(t, withHooks("use std::fmt;\nuse serde::Serialize;\n"))
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> std::fmt"), "std::fmt", "")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> serde::Serialize"), "serde::Serialize", "")
}

// Items declared inside an item macro get no fact, so an import of one lands
// on the directory module of the file declaring it, never on a guessed item.
func TestExtract_ImportOfAnItemWithoutAFactLandsOnItsDirectory(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"src/lib.rs":       "mod core;\nmod hooks;\n",
		"src/core/mod.rs":  "macro_rules! cfg_on { ($($i:item)*) => { $($i)* } }\ncfg_on! { pub fn hidden() {} }\n",
		"src/hooks/mod.rs": "use crate::core::hidden;\n",
	})
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::hidden"), "src/core", "")
	if _, _, causes := importsCensus(t, ff); causes["item_without_fact"] != 1 {
		t.Errorf("want item_without_fact counted once, got %v", causes)
	}
}

// `use crate::registry;` binds both `pub mod registry` and `pub fn registry`
// (tracing-subscriber's shape); the file uses the module, and a module leaf is
// what the import lands as.
func TestExtract_NamespaceTwinImportLandsAsTheModule(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate,
		"src/lib.rs":          "pub mod registry;\nmod hooks;\npub fn registry() {}\n",
		"src/registry/mod.rs": "pub struct Registry;\n",
		"src/hooks/mod.rs":    "use crate::registry;\n",
	})
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::registry"), "src/registry", "")
	if _, _, causes := importsCensus(t, ff); causes["namespace_twin"] != 1 {
		t.Errorf("want namespace_twin counted once, got %v", causes)
	}
}

// Across crates a private module is not visible, so a module and a function
// of one name are no twin there: the function is what the import binds
// (actix-web-codegen's route).
func TestExtract_PrivateModuleTwinReadFromAnotherCrateIsTheItem(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":               "[workspace]\nmembers = [\"app\", \"codegen\"]\n",
		"app/Cargo.toml":           "[package]\nname = \"app\"\n\n[dependencies]\ncodegen = { path = \"../codegen\" }\n",
		"app/src/main.rs":          "use codegen::route;\nfn main() {}\n",
		"codegen/Cargo.toml":       "[package]\nname = \"codegen\"\n",
		"codegen/src/lib.rs":       "mod route;\npub fn route() {}\n",
		"codegen/src/route/mod.rs": "pub fn inner() {}\n",
	})
	requireImport(t, dependencyNamed(t, ff, "app/src -> codegen::route"), "codegen/src.route", "codegen/src/lib.rs")
}

// A crate whose manifest directory holds only build.rs besides src/ once had
// its source root read as the manifest directory, so every crate:: import it
// could not place landed on ".". The root is the file Cargo names.
func TestExtract_RootBuildScriptNoLongerSendsCrateImportsToTheRoot(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"build.rs":         "fn main() {}\n",
		"src/main.rs":      "mod core;\nmod hooks;\nfn main() {}\n",
		"src/core/mod.rs":  "pub fn count() -> u32 { 1 }\n",
		"src/hooks/mod.rs": "use crate::core::*;\nuse crate::core::count;\n",
	})
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::count"), "src/core.count", "src/core/mod.rs")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::core::*"), "src/core", "")
}

// rtk's tests write `use super::*` in src/main.rs and `use super::super::x`
// in src/core/utils.rs. A test module's leaves are not resolved, and the walk
// took super for the parent directory: from src/main.rs, ".".
func TestExtract_SuperInATestModuleStartsAtItsOwnModule(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":        demoCrate,
		"build.rs":          "fn main() {}\n",
		"src/main.rs":       "mod core;\nfn main() {}\n#[cfg(test)]\nmod tests {\n    use super::*;\n}\n",
		"src/core/mod.rs":   "pub mod utils;\npub fn count() -> u32 { 1 }\n",
		"src/core/utils.rs": "pub fn trim() {}\n#[cfg(test)]\nmod tests {\n    use super::super::count;\n}\n",
	})
	requireImport(t, dependencyNamed(t, ff, "src -> super::*"), "src", "")
	requireImport(t, dependencyNamed(t, ff, "src/core -> super::super::count"), "src/core", "")
}

// ripgrep includes crates/core/index/enabled.rs through #[path], which no crate
// root reaches, so its crate:: leaves start at the nearest crate root above.
func TestExtract_CrateLeafOutsideTheModuleTreeStartsAtTheNearestCrateRoot(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":                   "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[[bin]]\nname = \"demo\"\npath = \"crates/core/main.rs\"\n",
		"build.rs":                     "fn main() {}\n",
		"crates/core/main.rs":          "mod flags;\nmod index;\nfn main() {}\n",
		"crates/core/flags/mod.rs":     "pub struct Args;\n",
		"crates/core/index/mod.rs":     "#[path = \"enabled.rs\"]\nmod imp;\n",
		"crates/core/index/enabled.rs": "use crate::flags::Args;\n",
	})
	requireImport(t, dependencyNamed(t, ff, "crates/core/index -> crate::flags::Args"), "crates/core/flags", "")
}

func TestExtract_RustImportsCensusCountsEveryInternalLeaf(t *testing.T) {
	ff := extractRustRepo(t, withHooks("use crate::core::{count, Tally};\nuse crate::util;\nuse crate::core::*;\nuse std::fmt;\nuse crate::gone::thing;\n"))
	detected, resolved, causes := importsCensus(t, ff)
	sum := 0
	for _, n := range causes {
		sum += n
	}
	// Five internal leaves in hooks and lib.rs's `pub use`; std::fmt is not
	// the repository's.
	if detected != 6 || resolved != 4 || resolved+sum != detected {
		t.Errorf("detected %d, resolved %d, causes %v", detected, resolved, causes)
	}
}

// ---- located refusals ---------------------------------------------------

func TestExtract_RefusedCallsAreCountedOnTheFactThatMadeThem(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": "fn count(v: u32) -> u32 { v }\npub fn tally(v: Vec<u32>) -> usize { let count = |x: u32| x; count(3); v.len(); v.iter().count() }\n",
	})
	f, _ := findFact(ff, "src.tally")
	if got := f.PropString("unresolved_calls"); got != "local_binding=1,receiver_expression=1,type_unresolved=2" {
		t.Errorf("want the refusals by cause on src.tally, got %q", got)
	}
	_, resolved, causes := rustCallsCensus(t, ff)
	located := 0
	for _, g := range ff {
		for _, pair := range strings.Split(g.PropString("unresolved_calls"), ",") {
			if _, n, ok := strings.Cut(pair, "="); ok {
				k, err := strconv.Atoi(n)
				if err != nil {
					t.Fatalf("%s: %v", pair, err)
				}
				located += k
			}
		}
	}
	total := 0
	for _, n := range causes {
		total += n
	}
	if located != total {
		t.Errorf("unresolved_calls sums to %d, rust:calls counts %d unresolved (resolved %d)", located, total, resolved)
	}
}

// ---- shared names by file -------------------------------------------------

func TestExtract_SharedNameCallBecomesADependencyByFile(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":         demoCrate,
		"src/lib.rs":         "mod twins;\npub fn via() -> u32 { twins::alpha::run() + twins::alpha::run() + twins::beta::run() }\n",
		"src/twins/mod.rs":   "pub mod alpha;\npub mod beta;\n",
		"src/twins/alpha.rs": "pub fn run() -> u32 { 1 }\n",
		"src/twins/beta.rs":  "pub fn run() -> u32 { 2 }\n",
	})
	requireRefused(t, ff, "src.via", "run")
	for _, file := range []string{"src/twins/alpha.rs", "src/twins/beta.rs"} {
		name := facts.CallByFileName("src/lib.rs", "src/twins.run", file)
		dep := dependencyNamed(t, ff, name)
		if dep.File != "src/lib.rs" || dep.PropString(facts.PropTargetFile) != file ||
			len(dep.Relations) != 1 || dep.Relations[0].Kind != facts.RelCalls || dep.Relations[0].Target != "src/twins.run" {
			t.Errorf("%s: want one calls relation to src/twins.run carrying %s, got %+v", name, file, dep)
		}
	}
}

// diesel's `dsl::select` is a type alias in dsl and a function a glob brings
// in: the leaf binds both, so neither is the item it imports.
func TestExtract_LeafNamingATypeAndAValueRefusesBoth(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":     demoCrate,
		"src/lib.rs":     "mod dsl;\nmod query;\nmod run;\n",
		"src/query.rs":   "pub fn select() -> u32 { 1 }\n",
		"src/dsl/mod.rs": "pub use crate::query::*;\npub type select = u32;\n",
		"src/run/mod.rs": "use crate::dsl::select;\npub fn go() -> u32 { select() }\n",
	})
	dep := dependencyNamed(t, ff, "src/run -> crate::dsl::select")
	if got := dep.PropString(facts.PropTargetFile); got != "" {
		t.Errorf("the leaf must not land on either declaration, got target_file %q -> %+v", got, dep.Relations)
	}
	_, _, causes := importsCensus(t, ff)
	if causes["type_value_twin"] != 1 {
		t.Errorf("want one type_value_twin, got %v", causes)
	}
}

// diesel_cli's tests declare `pub mod database` under cfg_attr paths beside a
// `pub fn database`: the module has no file the resolver finds, and the leaf
// may bind it, so the function is not the answer.
func TestExtract_ModuleWithoutAFileBesideAnItemRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":                     demoCrate,
		"src/lib.rs":                     "mod support;\nmod run;\n",
		"src/support/mod.rs":             "#[cfg_attr(feature = \"sqlite\", path = \"sqlite_database.rs\")]\npub mod database;\nmod sqlite_database;\npub fn database() -> u32 { 1 }\n",
		"src/support/sqlite_database.rs": "pub struct Database;\n",
		"src/run.rs":                     "use crate::support::database;\npub fn go() -> u32 { database() }\n",
	})
	dep := dependencyNamed(t, ff, "src -> crate::support::database")
	if got := dep.PropString(facts.PropTargetFile); got != "" {
		t.Errorf("the leaf must not land on the function, got target_file %q -> %+v", got, dep.Relations)
	}
	_, _, causes := importsCensus(t, ff)
	if causes["namespace_twin"] != 1 {
		t.Errorf("want one namespace_twin, got %v", causes)
	}
}

// diesel's dsl uses the function `select` by name beside a glob of a module a
// macro makes, which holds the type `select`. A use binds a name only in the
// namespaces it names, so the glob may fill the type namespace: read, the
// leaf names two declarations; unread, it may, and neither is the item.
func TestExtract_UseOfAValueBesideAGlobHoldingTheTypeRefuses(t *testing.T) {
	for label, dsl := range map[string]string{
		"read":   "pub use crate::query::select;\npub use crate::types::*;\n",
		"unread": "make_proxy!(proxy);\npub use crate::query::select;\npub use proxy::*;\n",
	} {
		ff := extractRustRepo(t, map[string]string{
			"Cargo.toml":     demoCrate,
			"src/lib.rs":     "mod dsl;\nmod query;\nmod run;\nmod types;\n",
			"src/query.rs":   "pub fn select() -> u32 { 1 }\n",
			"src/types.rs":   "pub type select = u32;\n",
			"src/dsl/mod.rs": dsl,
			"src/run/mod.rs": "use crate::dsl::select;\npub fn go() -> u32 { select() }\n",
		})
		dep := dependencyNamed(t, ff, "src/run -> crate::dsl::select")
		if got := dep.PropString(facts.PropTargetFile); got != "" {
			t.Errorf("%s: the leaf must not land on the function, got target_file %q", label, got)
		}
		if _, _, causes := importsCensus(t, ff); causes["type_value_twin"] != 1 {
			t.Errorf("%s: want one type_value_twin, got %v", label, causes)
		}
	}
}

// tracing-subscriber declares the function `registry` inside an item macro
// and the module `registry` as a directory: the leaf binds both, and the
// namespace twin lands as the module.
func TestExtract_MacroFunctionBesideAModuleIsANamespaceTwin(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate,
		"src/lib.rs":          "macro_rules! feature {\n    ($($i:item)*) => { $($i)* };\n}\npub mod registry;\nmod layer;\nfeature! {\n    pub fn registry() -> u32 { 1 }\n}\n",
		"src/registry/mod.rs": "pub struct Registry;\n",
		"src/layer/mod.rs":    "use crate::registry;\n",
	})
	requireImport(t, dependencyNamed(t, ff, "src/layer -> crate::registry"), "src/registry", "")
	if _, _, causes := importsCensus(t, ff); causes["namespace_twin"] != 1 {
		t.Errorf("want one namespace_twin, got %v", causes)
	}
}
