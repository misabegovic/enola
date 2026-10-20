package rustextractor

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const demoCrate = "[package]\nname = \"demo\"\n"

func extractRustRepo(t *testing.T, files map[string]string) []facts.Fact {
	t.Helper()
	repo, list := writeRustRepo(t, files)
	ff, err := New().Extract(context.Background(), repo, list)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	return ff
}

func callTargets(t *testing.T, ff []facts.Fact, owner string) []string {
	t.Helper()
	f, ok := findFact(ff, owner)
	if !ok {
		t.Fatalf("no fact named %s", owner)
	}
	var out []string
	for _, r := range f.Relations {
		if r.Kind == facts.RelCalls {
			out = append(out, r.Target)
		}
	}
	return out
}

func requireCall(t *testing.T, ff []facts.Fact, owner, target string) {
	t.Helper()
	for _, got := range callTargets(t, ff, owner) {
		if got == target {
			return
		}
	}
	t.Errorf("%s: want a calls relation to %s, got %v", owner, target, callTargets(t, ff, owner))
}

// requireRefused asserts the call kept its bare leaf and reached no
// qualified target ending in that name.
func requireRefused(t *testing.T, ff []facts.Fact, owner, leaf string) {
	t.Helper()
	bare := false
	for _, got := range callTargets(t, ff, owner) {
		if got == leaf {
			bare = true
		}
		if strings.HasSuffix(got, "."+leaf) {
			t.Errorf("%s: %s must be refused, got the edge %s", owner, leaf, got)
		}
	}
	if !bare {
		t.Errorf("%s: the refused call must keep its bare leaf %s, got %v", owner, leaf, callTargets(t, ff, owner))
	}
}

// rustCallsCensus reads the rust:calls extraction fact.
func rustCallsCensus(t *testing.T, ff []facts.Fact) (detected, resolved int, causes map[string]int) {
	t.Helper()
	for _, f := range ff {
		if f.Kind != facts.KindExtraction || f.Name != "rust:calls" {
			continue
		}
		coverage, _ := f.PropAny("edge_coverage").([]map[string]any)
		if len(coverage) != 1 {
			t.Fatalf("rust:calls carries one edge_coverage entry, got %v", f.PropAny("edge_coverage"))
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
	t.Fatalf("no rust:calls extraction fact")
	return 0, 0, nil
}

func requireCause(t *testing.T, ff []facts.Fact, cause string) {
	t.Helper()
	if _, _, causes := rustCallsCensus(t, ff); causes[cause] == 0 {
		t.Errorf("want the census to count %s, got %v", cause, causes)
	}
}

// ---- resolution rules -----------------------------------------------------

func TestExtract_PathAcrossFilesResolves(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":            demoCrate,
		"src/lib.rs":            "mod geometry;\npub fn run() -> u32 { crate::geometry::area(2) }\n",
		"src/geometry/mod.rs":   "mod solid;\npub fn area(x: u32) -> u32 { x }\n",
		"src/geometry/solid.rs": "pub fn volume(x: u32) -> u32 { super::area(x) }\n",
	})
	requireCall(t, ff, "src.run", "src/geometry.area")
	requireCall(t, ff, "src/geometry.volume", "src/geometry.area")
}

func TestExtract_UseAliasNamesTheItem(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate,
		"src/lib.rs":          "mod geometry;\nuse crate::geometry::area as surface;\npub fn run() -> u32 { surface(4) }\n",
		"src/geometry/mod.rs": "pub fn area(x: u32) -> u32 { x }\n",
	})
	requireCall(t, ff, "src.run", "src/geometry.area")
}

// A `use a::b::*` records the glob's path; reading only the `*` lost it, and
// every internal glob looked external.
func TestExtract_InternalGlobIsLookedInto(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"src/lib.rs":       "mod store;\nuse crate::store::*;\npub fn run() -> u32 { purge() }\n",
		"src/store/mod.rs": "pub fn purge() -> u32 { 0 }\n",
	})
	requireCall(t, ff, "src.run", "src/store.purge")
}

func TestExtract_BareCallResolvesThroughInternalImport(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":        demoCrate,
		"src/lib.rs":        "mod helper;\nuse self::helper::run;\npub fn caller() { run(); }\n",
		"src/helper/mod.rs": "pub fn run() {}\n",
	})
	requireCall(t, ff, "src.caller", "src/helper.run")
}

func TestExtract_LibPathFromTheManifestIsACrateRoot(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate + "[lib]\npath = \"lib/root.rs\"\n",
		"lib/root.rs":         "mod geometry;\npub fn run() -> u32 { crate::geometry::area(1) }\n",
		"lib/geometry/mod.rs": "pub fn area(x: u32) -> u32 { x }\n",
	})
	requireCall(t, ff, "lib.run", "lib/geometry.area")
}

func TestExtract_BinTargetPathIsACrateRoot(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate + "\n[[bin]]\nname = \"tool\"\npath = \"cli/tool.rs\"\n",
		"cli/tool.rs":      "mod util;\nfn main() { util::go(); }\n",
		"cli/util/mod.rs":  "pub fn go() {}\n",
		"src/lib.rs":       "pub fn lib() {}\n",
		"cli/stray/mod.rs": "pub fn go() {}\n",
	})
	requireCall(t, ff, "cli.main", "cli/util.go")
}

func TestExtract_DeclaredTestTargetIsACrateRoot(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":             demoCrate + "autotests = false\n\n[[test]]\nname = \"it\"\n",
		"src/lib.rs":             "pub fn lib() {}\n",
		"tests/it.rs":            "mod helpers;\npub fn check() -> u32 { crate::helpers::one() }\n",
		"tests/helpers/mod.rs":   "pub fn one() -> u32 { 1 }\n",
		"tests/other.rs":         "mod helpers;\npub fn other() -> u32 { crate::helpers::one() }\n",
		"tests/unused/mod.rs":    "pub fn one() -> u32 { 1 }\n",
		"tests/helpers/extra.rs": "pub fn extra() {}\n",
	})
	requireCall(t, ff, "tests.check", "tests/helpers.one")
	// autotests = false: tests/other.rs is no target, so nothing compiles it.
	requireRefused(t, ff, "tests.other", "one")
	requireCause(t, ff, "outside_module_tree")
}

func TestExtract_PatchedDependencyNamesTheWorkspaceCrate(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       "[workspace]\nmembers = [\"app\", \"util\"]\n\n[patch.crates-io]\nutil = { path = \"util\" }\n",
		"app/Cargo.toml":   "[package]\nname = \"app\"\n\n[dependencies]\nutil = \"1\"\nother = \"1\"\n",
		"app/src/main.rs":  "fn main() { util::shout(); other::shout(); }\n",
		"util/Cargo.toml":  "[package]\nname = \"util\"\n",
		"util/src/lib.rs":  "pub fn shout() {}\n",
		"other/Cargo.toml": "[package]\nname = \"other\"\n",
		"other/src/lib.rs": "pub fn shout() {}\n",
	})
	requireCall(t, ff, "app/src.main", "util/src.shout")
	// other is a registry dependency nobody patches: a crate of that name in
	// this repository is not the one app is built against.
	for _, got := range callTargets(t, ff, "app/src.main") {
		if got == "other/src.shout" {
			t.Errorf("a registry dependency must stay outside the repository, got %v", got)
		}
	}
}

func TestExtract_AssociatedFunctionWithItsImplInAnotherFile(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":            demoCrate,
		"src/lib.rs":            "mod geometry;\nuse geometry::Point;\npub fn run() -> Point { Point::origin() }\n",
		"src/geometry/mod.rs":   "mod point;\npub struct Point { pub x: u32 }\n",
		"src/geometry/point.rs": "use super::Point;\nimpl Point {\n    pub fn origin() -> Point { Point { x: 0 } }\n}\n",
	})
	requireCall(t, ff, "src.run", "src/geometry.Point.origin")
}

var pointFiles = map[string]string{
	"Cargo.toml":          demoCrate,
	"src/geometry/mod.rs": "pub struct Point { pub x: u32 }\nimpl Point {\n    pub fn norm(&self) -> u32 { self.x }\n}\n",
}

func withPoint(lib string) map[string]string {
	files := map[string]string{"src/lib.rs": "mod geometry;\nuse geometry::Point;\n" + lib}
	for k, v := range pointFiles {
		files[k] = v
	}
	return files
}

func TestExtract_TypedParameterReceiver(t *testing.T) {
	ff := extractRustRepo(t, withPoint("pub fn run(p: &Point) -> u32 { p.norm() }\n"))
	requireCall(t, ff, "src.run", "src/geometry.Point.norm")
}

func TestExtract_SelfFieldReceiver(t *testing.T) {
	ff := extractRustRepo(t, withPoint(
		"pub struct Canvas { origin: Point }\nimpl Canvas {\n    pub fn width(&self) -> u32 { self.origin.norm() }\n}\n"))
	requireCall(t, ff, "src.Canvas.width", "src/geometry.Point.norm")
}

func TestExtract_TypedLetReceiver(t *testing.T) {
	ff := extractRustRepo(t, withPoint("pub fn run(v: Point) -> u32 { let q: Point = v; q.norm() }\n"))
	requireCall(t, ff, "src.run", "src/geometry.Point.norm")
}

func TestExtract_StructLiteralLetReceiver(t *testing.T) {
	ff := extractRustRepo(t, withPoint("pub fn run() -> u32 { let r = Point { x: 1 }; r.norm() }\n"))
	requireCall(t, ff, "src.run", "src/geometry.Point.norm")
}

// A::run() with A declared in the file is a path, not a guess about a
// receiver: the per-file walk leaves it (TestAST_UnrelatedTypeMethodCall_
// NoFalseSelfEdge), and the crate-wide pass grounds it on A's method.
func TestExtract_TypePathCallResolvesToThatTypesMethod(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub struct A;
impl A {
    pub fn run(&self) {}
}
pub struct B;
impl B {
    pub fn call(&self) {
        A::run(&A);
    }
    pub fn run(&self) {}
}
`,
	})
	requireCall(t, ff, "src.B.call", "src.A.run")
	for _, got := range callTargets(t, ff, "src.B.call") {
		if got == "src.B.run" {
			t.Errorf("A::run must not resolve to B's method, got %v", got)
		}
	}
}

// The call rung reads the loop call lists from final targets: a pending call
// resolved by the pass is listed by its qualified name, not its bare leaf.
func TestExtract_LoopCallListsCarryResolvedTargets(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate,
		"src/lib.rs":          "mod geometry;\npub fn run(xs: &[u32]) { for x in xs { crate::geometry::area(*x); } }\n",
		"src/geometry/mod.rs": "pub fn area(x: u32) -> u32 { x }\n",
	})
	f, _ := findFact(ff, "src.run")
	in, _ := f.PropAny("calls_in_loop").([]string)
	scaling, _ := f.PropAny("calls_in_scaling_loop").([]string)
	if len(in) != 1 || in[0] != "src/geometry.area" || len(scaling) != 1 || scaling[0] != "src/geometry.area" {
		t.Errorf("want the loop lists to name src/geometry.area, got %v and %v", in, scaling)
	}
}

func TestExtract_RecursionThroughThePassIsRecorded(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":          demoCrate,
		"src/lib.rs":          "mod geometry;\n",
		"src/geometry/mod.rs": "pub fn walk(n: u32) -> u32 { if n == 0 { 0 } else { crate::geometry::walk(n - 1) } }\n",
	})
	f, _ := findFact(ff, "src/geometry.walk")
	if !f.PropBool("recursive_self") {
		t.Errorf("a call the pass grounds on the function itself is recursion, got %+v", f.Props)
	}
}

// ---- refusals ---------------------------------------------------------------

func TestExtract_ClosureShadowingAFunctionRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": "fn count(v: u32) -> u32 { v }\npub fn tally() -> u32 { let count = |v: u32| v + 1; count(3) }\n",
	})
	requireRefused(t, ff, "src.tally", "count")
	requireCause(t, ff, "local_binding")
}

// Rust reaches a method only through self. or Self::, so a bare reset()
// inside an impl is the free function, never the sibling method.
func TestExtract_BareCallNeverReachesAMethod(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub fn reset() {}
pub struct Counter;
impl Counter {
    pub fn reset(&self) {}
    pub fn tick(&self) { reset(); }
}
`,
	})
	requireCall(t, ff, "src.Counter.tick", "src.reset")
	for _, got := range callTargets(t, ff, "src.Counter.tick") {
		if got == "src.Counter.reset" {
			t.Errorf("a bare call must not reach the sibling method, got %v", got)
		}
	}
}

// A sibling-file function is not in scope without a use, whatever its
// directory: the same-directory guess is gone.
func TestExtract_SiblingFileFunctionIsNotInScope(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":     demoCrate,
		"src/lib.rs":     "mod helpers;\nmod caller;\n",
		"src/helpers.rs": "pub fn step() {}\n",
		"src/caller.rs":  "pub fn go() { step(); }\n",
	})
	requireRefused(t, ff, "src.go", "step")
	requireCause(t, ff, "not_in_scope")
}

func TestExtract_InherentAndTraitMethodOfOneNameRefuse(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub struct Point;
impl Point {
    pub fn grow(&self) -> u32 { 1 }
}
pub trait Grow { fn grow(&self) -> u32; }
impl Grow for Point {
    fn grow(&self) -> u32 { 2 }
}
pub fn run(p: &Point) -> u32 { p.grow() }
`,
	})
	requireRefused(t, ff, "src.run", "grow")
	requireCause(t, ff, "ambiguous_impl")
}

func TestExtract_TraitMethodByPathRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub trait Perimeter { fn perimeter_of(s: &Self) -> u32; }
pub struct Square;
impl Perimeter for Square {
    fn perimeter_of(_s: &Self) -> u32 { 4 }
}
pub fn measure(s: &Square) -> u32 { Square::perimeter_of(s) }
`,
	})
	requireRefused(t, ff, "src.measure", "perimeter_of")
	requireCause(t, ff, "trait_method_by_path")
}

func TestExtract_TraitMethodUnderABoundRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub trait Area { fn area(&self) -> u32 { 0 } }
pub struct Sq;
impl Area for Sq {}
pub fn total<T: Area>(s: &Sq, _t: T) -> u32 { s.area() }
`,
	})
	requireRefused(t, ff, "src.total", "area")
	requireCause(t, ff, "trait_method_under_bound")
}

func TestExtract_PinnedSelfWrapperMethodRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
use std::pin::Pin;
pub struct Pinned;
impl Pinned {
    pub fn get_mut(&mut self) -> u32 { 1 }
    pub fn poll_it(self: Pin<&mut Self>) -> u32 { self.get_mut(); 0 }
}
`,
	})
	requireRefused(t, ff, "src.Pinned.poll_it", "get_mut")
	requireCause(t, ff, "self_wrapped")
}

func TestExtract_ReceiverInAUserMacroRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
macro_rules! delegate { ($e:expr) => { $e }; }
pub struct S;
impl S {
    pub fn describe(&self) -> u32 { delegate!(self.describe()) }
}
`,
	})
	requireRefused(t, ff, "src.S.describe", "describe")
	requireCause(t, ff, "receiver_in_user_macro")
}

func TestExtract_CfgMacroItemRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
macro_rules! cfg_a { ($($i:item)*) => { $($i)* } }
macro_rules! cfg_b { ($($i:item)*) => { $($i)* } }
mod engine;
cfg_a! { pub struct Gauge; }
cfg_b! { use crate::engine::Gauge; }
pub fn sample() { Gauge::new(); }
`,
		"src/engine/mod.rs": "pub struct Gauge;\nimpl Gauge {\n    pub fn new() -> Gauge { Gauge }\n}\n",
	})
	requireRefused(t, ff, "src.sample", "new")
	requireCause(t, ff, "cfg_macro_item")
}

func TestExtract_CfgTestUseIsNotABinding(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"src/lib.rs":       "mod real;\nmod mocks;\n#[cfg(test)]\nuse mocks::spawn;\n#[cfg(not(test))]\nuse real::spawn;\npub fn run() { spawn(); }\n",
		"src/real/mod.rs":  "pub fn spawn() {}\n",
		"src/mocks/mod.rs": "pub fn spawn() {}\n",
	})
	requireCall(t, ff, "src.run", "src/real.spawn")
}

func TestExtract_TwoUsesOfOneNameRefuse(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":   demoCrate,
		"src/lib.rs":   "mod a;\nmod b;\n#[cfg(unix)]\nuse crate::a::open;\n#[cfg(windows)]\nuse crate::b::open;\npub fn run() { open(); }\n",
		"src/a/mod.rs": "pub fn open() {}\n",
		"src/b/mod.rs": "pub fn open() {}\n",
	})
	requireRefused(t, ff, "src.run", "open")
	requireCause(t, ff, "import_conflict")
}

func TestExtract_SpecializedImplRefusesAMethodCall(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub struct Cached<T>(T);
pub struct Stmt;
impl Cached<Stmt> {
    pub fn execute(&self) {}
    pub fn open() -> Cached<Stmt> { Cached(Stmt) }
}
pub fn run(c: &Cached<Stmt>) { c.execute(); Cached::open(); }
`,
	})
	requireRefused(t, ff, "src.run", "execute")
	requireCause(t, ff, "impl_needs_type_args")
	// A path call writes no argument, so rustc infers it from the one impl.
	requireCall(t, ff, "src.run", "src.Cached.open")
}

func TestExtract_TraitWithTypeArgumentsRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub trait Fragment<DB> { fn walk(&self); }
pub struct Pg;
pub struct Query;
impl Fragment<Pg> for Query {
    fn walk(&self) {}
}
pub fn run(q: &Query) { q.walk() }
`,
	})
	requireRefused(t, ff, "src.run", "walk")
	requireCause(t, ff, "trait_impl_needs_type_args")
}

func TestExtract_BlanketImplOnAReferenceRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
pub trait Message { fn headers(&self) -> u32; }
impl<T: Message> Message for &mut T {
    fn headers(&self) -> u32 { 0 }
}
pub struct Res;
impl Res {
    pub fn headers(&self) -> u32 { 1 }
}
pub fn run(r: &mut Res) -> u32 { r.headers() }
`,
	})
	requireRefused(t, ff, "src.run", "headers")
	requireCause(t, ff, "blanket_impl_on_reference")
}

func TestExtract_TwinInlineModulesRefuseTheirSharedNames(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
#[cfg(feature = "std")]
mod compat {
    pub fn abort() {}
    pub fn only_std() {}
}
#[cfg(not(feature = "std"))]
mod compat {
    pub use core::intrinsics::abort;
}
pub fn run() { compat::abort(); compat::only_std(); }
`,
	})
	requireRefused(t, ff, "src.run", "abort")
	requireCause(t, ff, "merged_module_twin")
	requireCall(t, ff, "src.run", "src.compat.only_std")
}

// An item macro whose body parses as items is read as items; a doc comment
// inside it is not a `use`, as reading its text once made it.
func TestExtract_ItemMacroBodyIsReadAsItems(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate,
		"src/lib.rs": `
macro_rules! cfg_on { ($($i:item)*) => { $($i)* } }
cfg_on! {
    /// Read this, then use crate::decoy::load; for the old path.
    mod store;
    pub use store::load;
}
mod decoy;
pub fn run() -> u32 { load() }
`,
		"src/store/mod.rs": "pub fn load() -> u32 { 1 }\n",
		"src/decoy/mod.rs": "pub fn load() -> u32 { 2 }\n",
	})
	requireCall(t, ff, "src.run", "src/store.load")
}

func TestExtract_SharedNameKeepsTheBareLeaf(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":         demoCrate,
		"src/lib.rs":         "mod twins;\nuse twins::alpha::run;\npub fn via() -> u32 { run() }\n",
		"src/twins/mod.rs":   "pub mod alpha;\npub mod beta;\n",
		"src/twins/alpha.rs": "pub fn run() -> u32 { 1 }\npub fn again() -> u32 { run() }\n",
		"src/twins/beta.rs":  "pub fn run() -> u32 { 2 }\n",
	})
	requireRefused(t, ff, "src.via", "run")
	requireRefused(t, ff, "src/twins.again", "run")
	if _, _, causes := rustCallsCensus(t, ff); causes["shared_name"] < 2 {
		t.Errorf("both calls refuse as shared_name, got %v", causes)
	}
}

func TestExtract_TestRefHoldsEachFinalTargetOnce(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"src/lib.rs":       "pub mod twins;\n",
		"src/twins/mod.rs": "pub mod alpha;\npub mod beta;\n",
		"src/twins/alpha.rs": `
pub fn run() -> u32 { 1 }
#[cfg(test)]
mod tests {
    #[test]
    fn t() { run(); super::run(); }
}
`,
		"src/twins/beta.rs": "pub fn run() -> u32 { 2 }\n",
	})
	for _, f := range findFactsByKind(ff, facts.KindTestRef) {
		if f.File != "src/twins/alpha.rs" {
			continue
		}
		n := 0
		for _, r := range f.Relations {
			if strings.HasSuffix(r.Target, "run") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("test_ref must hold run once, got %+v", f.Relations)
		}
		return
	}
	t.Fatal("no test_ref fact for alpha.rs")
}

func TestExtract_FileNoCrateRootReachesIsOutsideTheTree(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":       demoCrate,
		"src/lib.rs":       "pub fn one() -> u32 { 1 }\n",
		"stray/fixture.rs": "pub fn f() -> u32 { crate::one() }\n",
	})
	requireRefused(t, ff, "stray.f", "one")
	requireCause(t, ff, "outside_module_tree")
}

// The held-out rung's nine wrong edges: a local cfg item and a cfg `use` bind
// one name, and which is live depends on features the extractor does not
// evaluate (tracing-subscriber's Style).
func TestExtract_CfgItemBesideCfgUseRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml": demoCrate + "\n[features]\ndefault = [\"x\"]\nx = []\n",
		"src/lib.rs": `
mod other;
#[cfg(feature = "x")]
use other::S;
#[cfg(not(feature = "x"))]
pub struct S;
#[cfg(not(feature = "x"))]
impl S {
    pub fn new() -> S { S }
}
pub fn make() -> S { S::new() }
`,
		"src/other/mod.rs": "pub struct S;\nimpl S {\n    pub fn new() -> S { S }\n}\n",
	})
	requireRefused(t, ff, "src.make", "new")
	requireCause(t, ff, "cfg_item_import_twin")
}

// ---- the census and determinism ----------------------------------------------

var censusFiles = map[string]string{
	"Cargo.toml":          demoCrate,
	"src/lib.rs":          "mod geometry;\nuse geometry::Point;\npub fn run(p: &Point, v: Vec<u32>) -> usize { let f = |x: u32| x; f(1); p.norm(); std::mem::drop(1); v.len() }\nconst C: u32 = geometry::area(1);\n",
	"src/geometry/mod.rs": "pub struct Point { pub x: u32 }\nimpl Point {\n    pub fn norm(&self) -> u32 { self.x }\n}\npub const fn area(x: u32) -> u32 { x }\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() { super::area(1); area(2); }\n}\n",
}

func TestExtract_RustCallsCensusCountsEveryRelation(t *testing.T) {
	ff := extractRustRepo(t, censusFiles)
	relations := 0
	for _, f := range ff {
		if lang, _ := f.PropAny("language").(string); lang != "rust" {
			continue
		}
		for _, r := range f.Relations {
			if r.Kind == facts.RelCalls {
				relations++
			}
		}
	}
	detected, resolved, causes := rustCallsCensus(t, ff)
	sum := 0
	for cause, n := range causes {
		if cause == "unclassified" {
			t.Errorf("every relation carries a named cause, got %d unclassified", n)
		}
		sum += n
	}
	if detected != relations || resolved+sum != detected {
		t.Errorf("detected %d, resolved %d, causes %v (%d) against %d calls relations", detected, resolved, causes, sum, relations)
	}
}

func TestExtract_FactsIdenticalAtOneAndSixteenWorkers(t *testing.T) {
	read := func(procs int) string {
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
		ff := extractRustRepo(t, censusFiles)
		lines := make([]string, 0, len(ff))
		for _, f := range ff {
			if f.Kind == facts.KindExtraction {
				f.File = ""
			}
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(raw))
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}
	if one, sixteen := read(1), read(16); one != sixteen {
		t.Errorf("facts differ between one worker and sixteen")
	}
}

// ripgrep's printer nests `fn replace_separator` in a method body, where its
// fact is named after the method's receiver. The pass named the call after
// the module, `src.helper`, which another file declares once: the call linked
// to that file's function. A name not declared in the file the pass chose is
// refused, and makes no dependency fact.
func TestExtract_CallNamedForAFileThatDoesNotDeclareItRefuses(t *testing.T) {
	ff := extractRustRepo(t, map[string]string{
		"Cargo.toml":   demoCrate,
		"src/lib.rs":   "mod other;\npub struct Path;\nimpl Path {\n    pub fn go(&self) -> u32 {\n        fn helper() -> u32 { 1 }\n        helper()\n    }\n}\n",
		"src/other.rs": "pub fn helper() -> u32 { 2 }\n",
	})
	if got := callTargets(t, ff, "src.Path.go"); len(got) != 1 || got[0] != "helper" {
		t.Errorf("the call keeps its written leaf, got %v", got)
	}
	f, _ := findFact(ff, "src.Path.go")
	if got := f.PropString("unresolved_calls"); got != "target_without_fact=1" {
		t.Errorf("want target_without_fact=1, got %q", got)
	}
	for _, g := range ff {
		if g.Kind == facts.KindDependency && g.PropString(facts.PropTargetFile) != "" && len(g.Relations) == 1 && g.Relations[0].Kind == facts.RelCalls {
			t.Errorf("no dependency fact is made for it, got %s", g.Name)
		}
	}
}

// ---- a name looked up once per depth -----------------------------------------

// Two modules that glob each other, diesel's shape: every name either holds is
// found through the other, and a name neither holds walks the cycle to the
// depth cap and is refused as an unread glob would be.
var globCycleFiles = map[string]string{
	"Cargo.toml":   demoCrate,
	"src/lib.rs":   "mod a;\nmod b;\nmod callers;\nmod hooks;\n",
	"src/a/mod.rs": "pub use crate::b::*;\npub fn alpha() -> u32 { 1 }\n",
	"src/b/mod.rs": "pub use crate::a::*;\npub fn beta() -> u32 { 2 }\n",
	"src/callers/mod.rs": "pub fn beta_through_a() -> u32 { crate::a::beta() }\n" +
		"pub fn alpha_through_b() -> u32 { crate::b::alpha() }\n" +
		"pub fn alpha_at_home() -> u32 { crate::a::alpha() }\n" +
		"pub fn neither() -> u32 { crate::a::gamma() }\n",
	"src/hooks/mod.rs": "use crate::a::beta;\nuse crate::b::alpha;\n",
}

func TestExtract_GlobCycleResolvesThroughEitherModule(t *testing.T) {
	ff := extractRustRepo(t, globCycleFiles)
	requireCall(t, ff, "src/callers.beta_through_a", "src/b.beta")
	requireCall(t, ff, "src/callers.alpha_through_b", "src/a.alpha")
	requireCall(t, ff, "src/callers.alpha_at_home", "src/a.alpha")
	requireRefused(t, ff, "src/callers.neither", "gamma")
	requireCause(t, ff, "glob_import")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::a::beta"), "src/b.beta", "src/b/mod.rs")
	requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::b::alpha"), "src/a.alpha", "src/a/mod.rs")
}

// globChainFiles is a chain of globs m0 -> m1 -> ... -> m8, where m8 declares
// deep, and two modules p and q that each glob m2, so the chain below m2 is
// reached by several paths. The depth cap stops a walk from m0 one module
// short of m8, and a walk from m1 arrives: the same module and name are asked
// at two depths with two answers, so an answer is only ever reused at the
// depth it was walked at. Both orders of asking are read, because whichever
// question came first would decide the other for a lookup that forgot its
// depth.
func globChainFiles(callers string) map[string]string {
	files := map[string]string{
		"Cargo.toml":         demoCrate,
		"src/lib.rs":         "mod m0;\nmod m1;\nmod m2;\nmod m3;\nmod m4;\nmod m5;\nmod m6;\nmod m7;\nmod m8;\nmod p;\nmod q;\nmod callers;\nmod hooks;\n",
		"src/m8/mod.rs":      "pub fn deep() -> u32 { 8 }\n",
		"src/p/mod.rs":       "pub use crate::m2::*;\n",
		"src/q/mod.rs":       "pub use crate::m2::*;\n",
		"src/callers/mod.rs": callers,
		"src/hooks/mod.rs":   "use crate::p::deep;\nuse crate::q::deep as also;\nuse crate::m1::deep as near;\nuse crate::m0::deep as far;\n",
	}
	for i := 0; i < 8; i++ {
		files["src/m"+strconv.Itoa(i)+"/mod.rs"] = "pub use crate::m" + strconv.Itoa(i+1) + "::*;\n"
	}
	return files
}

func TestExtract_GlobChainAnswersEachDepthAsAFreshWalkWould(t *testing.T) {
	near := "pub fn near() -> u32 { crate::m1::deep() }\n"
	far := "pub fn far() -> u32 { crate::m0::deep() }\n"
	paths := "pub fn through_p() -> u32 { crate::p::deep() }\npub fn through_q() -> u32 { crate::q::deep() }\n"
	for name, callers := range map[string]string{"near first": near + far + paths, "far first": far + near + paths, "paths first": paths + far + near} {
		t.Run(name, func(t *testing.T) {
			ff := extractRustRepo(t, globChainFiles(callers))
			requireCall(t, ff, "src/callers.near", "src/m8.deep")
			requireRefused(t, ff, "src/callers.far", "deep")
			requireCall(t, ff, "src/callers.through_p", "src/m8.deep")
			requireCall(t, ff, "src/callers.through_q", "src/m8.deep")
			requireCause(t, ff, "glob_import")
			requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::p::deep"), "src/m8.deep", "src/m8/mod.rs")
			requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::q::deep"), "src/m8.deep", "src/m8/mod.rs")
			requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::m1::deep"), "src/m8.deep", "src/m8/mod.rs")
			requireImport(t, dependencyNamed(t, ff, "src/hooks -> crate::m0::deep"), "src/m0", "")
		})
	}
}

// A question asked again is answered from memory: with the item removed after
// the first answer, only a remembered answer still finds it, and the same
// question one level deeper is its own and is walked.
func TestResolver_RemembersAnAnswerAtTheDepthItWasWalked(t *testing.T) {
	scope := &rustModScope{fns: map[string]bool{"count": true}}
	r := &rustResolver{files: map[string]*rustFileRecs{
		"src/lib.rs": {file: "src/lib.rs", dir: "src", scopes: map[string]*rustModScope{"": scope}},
	}}
	root := rustMod{file: "src/lib.rs"}
	first := r.lookup(root, "count", nsValue, 0)
	if first.kind != entFn || first.fact != "src.count" {
		t.Fatalf("want the function src.count, got %+v", first)
	}
	delete(scope.fns, "count")
	if again := r.lookup(root, "count", nsValue, 0); again != first {
		t.Errorf("the same question is answered from memory: want %+v, got %+v", first, again)
	}
	if deeper := r.lookup(root, "count", nsValue, 1); deeper.kind != entNone {
		t.Errorf("a question at another depth is walked afresh: want nothing, got %+v", deeper)
	}
}
