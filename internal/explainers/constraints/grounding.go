package constraints

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// Component membership resolves an edge's target by exact fact name, and a
// file-granular import target names no fact: module facts are named by directory
// (`lib`), symbol facts by `dir.symbol` (`lib.app`), while the TS extractor
// measures `require('./application')` as an imports edge onto the PATH
// `lib/application`. So the dominant dependency mechanism of a classic Node
// codebase was measured and unenforceable, and the silence read as compliance
// (finding 0010).
//
// The grounding below is the contained fix: a target that names no fact, but
// does name a file this repository measured, joins the component that file
// belongs to. It is strictly subordinate — every caller asks exact-name
// membership first and only falls back here — so no rule whose target already
// grounds by name can change verdict. It is scoped to one repository, because an
// extension-less path is only meaningful relative to the tree it was written in.
// And it applies to imports edges only: a calls or implements target is a symbol
// name, never a path, so resolving one as a file could only ever be a guess.

// importModuleExts are the source extensions an extension-less import target may
// have elided, in the resolver's own order (mirroring the TS extractor's).
var importModuleExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".vue", ".svelte", ".gts", ".gjs"}

// groundSkipConfidence caps the ungroundable-target advisory, for the same
// reason the reach-skip advisory sits at 0.4: it reports that no verdict was
// reached for those targets, and silence there must never read as compliance.
const groundSkipConfidence = 0.4

// grounding indexes what each repository measured: every file, and every fact
// name. The names half is what makes "resolves to nothing measured" a statement
// about the snapshot rather than about one component.
type grounding struct {
	files map[string]map[string]bool
	names map[string]bool
}

func newGrounding(store *facts.Store) *grounding {
	g := &grounding{files: map[string]map[string]bool{}, names: map[string]bool{}}
	// FactsRef, not All: this only reads, and retains nothing but two string sets.
	for _, f := range store.FactsRef() {
		if f.Name != "" {
			g.names[f.Name] = true
		}
		if f.File == "" {
			continue
		}
		set := g.files[f.Repo]
		if set == nil {
			set = map[string]bool{}
			g.files[f.Repo] = set
		}
		set[f.File] = true
	}
	return g
}

// resolve returns the measured file an import target names, in the canonical
// form the fact carries it. Both the bare and the repo-labelled shapes are
// tried, because a target is written repo-relative while an append-mode
// snapshot's files are label-prefixed.
func (g *grounding) resolve(target, repo string) (string, bool) {
	set := g.files[repo]
	if set == nil || target == "" {
		return "", false
	}
	bases := []string{target}
	if repo != "" {
		bases = append(bases, repo+"/"+target)
	}
	for _, base := range bases {
		if set[base] {
			return base, true
		}
		for _, ext := range importModuleExts {
			if p := base + ext; set[p] {
				return p, true
			}
		}
		// A directory import resolves to its index file, exactly as the module
		// resolver the extractor mirrors does.
		for _, ext := range importModuleExts {
			if p := base + "/index" + ext; set[p] {
				return p, true
			}
		}
	}
	return "", false
}

// inComponent reports whether an imports edge whose target named no member
// resolves to a measured file inside the named component. A name-narrowed or
// pattern-less component never grounds this way: the join is the component's own
// match globs, and a component that declares none has nothing to join against.
func (g *grounding) inComponent(rel facts.Relation, from facts.Fact, name string, components map[string]component) bool {
	if rel.Kind != facts.RelImports || name == "" {
		return false
	}
	c, declared := components[name]
	if !declared || c.namePattern != "" || len(c.match) == 0 {
		return false
	}
	if c.service != "" && from.Repo != c.service {
		return false
	}
	return g.resolvedPathIn(rel, from, func(path string) bool {
		return matchConstraintPath(path, c.match)
	})
}

// resolvedPathIn resolves an imports target to a measured file and applies the
// caller's test to it, in both the label-prefixed and repo-relative forms — the
// same double join matchConstraintFile uses, because a single trimmed form
// mis-fires when a real path starts with the repo's own name.
func (g *grounding) resolvedPathIn(rel facts.Relation, from facts.Fact, ok func(string) bool) bool {
	if rel.Kind != facts.RelImports {
		return false
	}
	path, resolved := g.resolve(rel.Target, from.Repo)
	if !resolved {
		return false
	}
	if ok(path) {
		return true
	}
	if from.Repo != "" {
		if trimmed := strings.TrimPrefix(path, from.Repo+"/"); trimmed != path {
			return ok(trimmed)
		}
	}
	return false
}

// resolves reports whether an imports target names a measured file at all —
// the resolvability question, asked only after the target failed to name a fact.
func (g *grounding) resolves(rel facts.Relation, from facts.Fact) bool {
	if rel.Kind != facts.RelImports {
		return false
	}
	_, ok := g.resolve(rel.Target, from.Repo)
	return ok
}

// groundedMembers names the members an imports edge lands on when its target
// names no member but resolves to a file those members were measured in. An
// inbound edge demanded of a member is satisfied by reaching the file the member
// lives in, since that is the only thing the target can name.
func groundedMembers(rel facts.Relation, from facts.Fact, memberFacts []facts.Fact, g *grounding) []string {
	if rel.Kind != facts.RelImports {
		return nil
	}
	path, ok := g.resolve(rel.Target, from.Repo)
	if !ok {
		return nil
	}
	var out []string
	for _, m := range memberFacts {
		if m.File == path {
			out = append(out, m.Name)
		}
	}
	return out
}

// ungroundable reports an imports edge whose target names no measured fact AND
// resolves to no measured file — a target this snapshot cannot answer for at
// all, most often a package outside the tree. Callers count these rather than
// dropping them, because a target nobody could resolve is a verdict not reached.
func (g *grounding) ungroundable(rel facts.Relation, from facts.Fact) bool {
	if rel.Kind != facts.RelImports || g.names[rel.Target] {
		return false
	}
	_, resolved := g.resolve(rel.Target, from.Repo)
	return !resolved
}

// groundSkipInsight names how many import targets a rule reached no verdict on,
// with a sample, so the residue is counted rather than swallowed.
func groundSkipInsight(r rule, targets map[string]bool) facts.Insight {
	named := make([]string, 0, len(targets))
	for t := range targets {
		named = append(named, t)
	}
	sort.Strings(named)
	sample := named
	if len(sample) > groundSkipSample {
		sample = sample[:groundSkipSample]
	}
	return facts.Insight{
		Title:       fmt.Sprintf("Constraint %s reached no verdict on %d import target(s) naming nothing measured", r.id, len(named)),
		Description: fmt.Sprintf("The rule walks imports edges, and %d of their targets name neither a measured fact nor a measured file in the importing repository — typically a package outside the tree. Those edges were skipped rather than verdicted: nothing was resolved, so nothing is claimed. This advisory exists so that the skip cannot be read as compliance. Sample: %s. Because: %s", len(named), strings.Join(sample, ", "), r.because),
		Confidence:  groundSkipConfidence,
		Evidence:    []facts.Evidence{{Fact: "rule: " + r.id, Detail: "declared in " + r.source}},
		Actions: []string{
			"Ignore the count when the targets are third-party packages the rule was never meant to reach",
			"Widen the snapshot to the repository holding those files if the rule should verdict them",
		},
	}
}

// groundSkipSample bounds the named sample: the count is the finding, the names
// are there to make it checkable.
const groundSkipSample = 8
