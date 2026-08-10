// Package constraints verdicts declared constraint rules against the measured
// graph — the enforcement half of the components/rules vocabulary pages
// declare. A component resolves to the measured facts its match patterns
// select (the same bounded path dialect declared layers use); a rule forbids
// one component reaching another via a named edge kind.
//
// Everything here is exact and fail-closed: membership is path equality or a
// declared subtree, target resolution is exact fact-name match — an edge whose
// target string does not name a member is no violation, never a guess — and a
// violation is a decided-rule breach at confidence 1.0, because the rule was
// stated and the edge is measured. The one advisory this package emits is the
// zero-member component note, so a selector that matches nothing is visible
// instead of silently satisfying every rule that names it.
package constraints

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// emptyComponentConfidence caps the dead-selector advisory: a component that
// matches nothing may be a moved tree or a selector wrong from the start, and
// this finding cannot tell which — it exists so that vacuous compliance never
// reads as compliance.
const emptyComponentConfidence = 0.4

// memberKinds are the measured fact kinds a component selector ranges over.
var memberKinds = []string{facts.KindModule, facts.KindSymbol, facts.KindRoute, facts.KindStorage}

// Explainer verdicts constraint-rule intent facts against measured edges.
type Explainer struct{}

// New creates the explainer.
func New() *Explainer { return &Explainer{} }

// Name returns the explainer identifier; `check --fail-on=constraints`
// selects it by this name.
func (e *Explainer) Name() string { return "constraints" }

type component struct {
	name        string
	match       []string
	kind        string
	namePattern string
	source      string
}

type rule struct {
	id, forbid, to, via, because, source string
}

// Explain resolves each declared component to its member facts, then emits one
// proof-class violation per measured edge a rule forbids, plus one advisory
// per component whose selector matched nothing.
func (e *Explainer) Explain(ctx context.Context, store *facts.Store) ([]facts.Insight, error) {
	components := map[string]component{}
	var rules []rule
	for _, f := range store.ByKind(facts.KindIntent) {
		switch f.PropString("intent_kind") {
		case "component":
			c := component{
				name:        f.PropString("component"),
				match:       strings.Fields(f.PropString("match")),
				kind:        f.PropString("kind"),
				namePattern: f.PropString("name_pattern"),
				source:      f.PropString("source"),
			}
			components[c.name] = c
		case "rule":
			rules = append(rules, rule{
				id:      f.PropString("rule"),
				forbid:  f.PropString("forbid"),
				to:      f.PropString("to"),
				via:     f.PropString("via"),
				because: f.PropString("because"),
				source:  f.PropString("source"),
			})
		}
	}
	if len(components) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(components))
	for n := range components {
		names = append(names, n)
	}
	sort.Strings(names)

	// Membership is a set of canonical fact NAMES, because a name is the only
	// thing an edge's target string can be matched against exactly; the facts
	// themselves are kept alongside, because they carry the edges to walk and
	// the file each violation is evidenced with.
	members := map[string]map[string]bool{}
	memberFacts := map[string][]facts.Fact{}
	for _, name := range names {
		c := components[name]
		members[name] = map[string]bool{}
		for _, kind := range memberKinds {
			if c.kind != "" && c.kind != kind {
				continue
			}
			for _, f := range store.ByKind(kind) {
				if !matchConstraintFile(f, c.match) {
					continue
				}
				if c.namePattern != "" && f.Name != c.namePattern {
					continue
				}
				members[name][f.Name] = true
				memberFacts[name] = append(memberFacts[name], f)
			}
		}
		// Store order reflects concurrent extraction; the walk below must not.
		sort.Slice(memberFacts[name], func(i, j int) bool {
			if memberFacts[name][i].Name != memberFacts[name][j].Name {
				return memberFacts[name][i].Name < memberFacts[name][j].Name
			}
			return memberFacts[name][i].File < memberFacts[name][j].File
		})
	}

	sort.Slice(rules, func(i, j int) bool { return rules[i].id < rules[j].id })

	var insights []facts.Insight
	for _, r := range rules {
		toSet := members[r.to]
		for _, f := range memberFacts[r.forbid] {
			for _, rel := range f.Relations {
				if rel.Kind != r.via || !toSet[rel.Target] {
					continue
				}
				insights = append(insights, facts.Insight{
					Title:       fmt.Sprintf("Constraint %s violated: %s -> %s via %s", r.id, f.Name, rel.Target, r.via),
					Description: fmt.Sprintf("%s must not reach %s via %s, and the graph measures exactly this edge. The rule is declared, both memberships are exact, so this is a decided-rule breach, not a heuristic. Because: %s", r.forbid, r.to, r.via, r.because),
					Confidence:  1.0,
					Evidence: []facts.Evidence{{
						File:   f.File,
						Symbol: f.Name,
						Fact:   rel.Target,
						Detail: "forbidden " + r.via + " edge",
					}},
					Actions: []string{
						"Remove or reroute the edge if the rule stands",
						"Amend the rule on its declaring page if the decision behind it changed",
					},
				})
			}
		}
	}

	for _, name := range names {
		if len(members[name]) > 0 {
			continue
		}
		c := components[name]
		insights = append(insights, facts.Insight{
			Title:       fmt.Sprintf("Constraint component %s matches nothing", name),
			Description: fmt.Sprintf("The component's match patterns (%s) select no measured fact, so every rule naming it holds vacuously — a dead selector enforcing nothing. Either the code moved out from under the patterns or the selector never matched; this advisory exists so that silence cannot be read as compliance.", strings.Join(c.match, ", ")),
			Confidence:  emptyComponentConfidence,
			Evidence:    []facts.Evidence{{Fact: "component: " + name, Detail: "declared in " + c.source}},
			Actions: []string{
				"Fix the match patterns if the code moved",
				"Remove the component and the rules naming it if the decision is retired",
			},
		})
	}

	sort.Slice(insights, func(i, j int) bool { return insights[i].Title < insights[j].Title })
	return insights, nil
}

// matchConstraintPath applies the bounded glob dialect: exact path, or
// `prefix/**` matching the prefix's whole subtree (and the prefix itself).
// Copied from the declared-layer matcher rather than shared, because that
// ceiling is each vocabulary's own decision — widening one must not silently
// widen the other.
func matchConstraintPath(path string, patterns []string) bool {
	for _, g := range patterns {
		if prefix, ok := strings.CutSuffix(g, "/**"); ok {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
			continue
		}
		if path == g {
			return true
		}
	}
	return false
}

// matchConstraintFile joins a fact's file against the patterns in both the
// label-prefixed and repo-relative forms — the same double join intentcheck's
// anchors use, because a single trimmed form mis-fires when a real path starts
// with the repo's own name. A fact with no file matches nothing: fail closed.
func matchConstraintFile(f facts.Fact, patterns []string) bool {
	if f.File == "" {
		return false
	}
	if matchConstraintPath(f.File, patterns) {
		return true
	}
	if f.Repo != "" {
		if trimmed := strings.TrimPrefix(f.File, f.Repo+"/"); trimmed != f.File {
			return matchConstraintPath(trimmed, patterns)
		}
	}
	return false
}
