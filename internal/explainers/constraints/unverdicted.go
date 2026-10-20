package constraints

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// The reasons a declared rule reaches no verdict against a snapshot, as a
// closed set in the order the explainer asks them. A rule that reached none
// has said nothing about the code, and a reader that learns that only from a
// finding's title has to parse prose the contract does not cover.
const (
	// UnverdictedAmbiguousComponent: a union declares a component the rule
	// names in two or more other repositories and not in the rule's own.
	UnverdictedAmbiguousComponent = "ambiguous_component"
	// UnverdictedServiceAbsent: a component the rule names selects a service
	// this snapshot does not hold, so the rule is unasked rather than kept.
	UnverdictedServiceAbsent = "service_absent"
	// UnverdictedSelectorUnevaluable: a component the rule names selects by a
	// predicate this snapshot cannot evaluate.
	UnverdictedSelectorUnevaluable = "selector_unevaluable"
	// UnverdictedRoleUnreachable: a component sits in an edge role that
	// resolves nothing on the side the rule reads it from.
	UnverdictedRoleUnreachable = "role_unreachable"
	// UnverdictedFormUnevaluable: the rule's form needs a measurement this
	// snapshot does not carry (resolved ancestry, access edges, a provider).
	UnverdictedFormUnevaluable = "form_unevaluable"
	// UnverdictedSkipped: the rule was skipped whole: its bounded walk was too
	// large, its antecedent selected nobody, or no member's edges were
	// measurable.
	UnverdictedSkipped = "skipped"
	// UnverdictedMatchesNothing: the component the rule is about selects no
	// measured fact, so the rule holds vacuously.
	UnverdictedMatchesNothing = "matches_nothing"
)

// UnverdictedReasons is the closed set above, in the order a rule is tested
// against it; the first that applies is the one reported.
var UnverdictedReasons = []string{
	UnverdictedAmbiguousComponent,
	UnverdictedServiceAbsent,
	UnverdictedSelectorUnevaluable,
	UnverdictedRoleUnreachable,
	UnverdictedFormUnevaluable,
	UnverdictedSkipped,
	UnverdictedMatchesNothing,
}

// Unverdict is why one rule reached no verdict: the reason from the closed set
// and a sentence naming the component, service or measurement it turned on.
type Unverdict struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// UnverdictedRules names every declared rule in the store that reached no
// verdict, with its reason. findings are the insights the same store's
// explainer run produced; the skips and refusals a verdict pass decides at
// run time are read back from them, and everything decided before a verdict
// pass runs is recomputed here from the store with the explainer's own
// resolution, so the answer cannot disagree with the findings beside it.
//
// A rule over a component that matches nothing is unverdicted only where an
// empty membership leaves the rule nothing to say. forbid_fact and cap are
// satisfied by one; a forbid_cycles rule needs two parts with members; and a
// role whose empty resolution the rule reads as a breach (owners, only,
// except, steps, require_edge's to) verdicted by breaching.
func UnverdictedRules(store *facts.Store, findings []facts.Insight) map[string]Unverdict {
	out := map[string]Unverdict{}
	components, rules := declarations(store)
	if len(rules) == 0 {
		return out
	}
	unasked := unaskedComponents(store, components)
	unevaluables := unevaluableSelectors(store, components, unasked)
	unevaluable := map[string]bool{}
	unevaluableProblem := map[string]string{}
	for _, u := range unevaluables {
		unevaluable[u.Component] = true
		if _, seen := unevaluableProblem[u.Component]; !seen {
			unevaluableProblem[u.Component] = u.Problem()
		}
	}
	members := map[string]map[string]bool{}
	memberFacts := map[string][]facts.Fact{}
	carried := map[string][]facts.Fact{}
	for name, c := range components {
		members[name], memberFacts[name] = resolveMembership(store, c)
		sources := append([]facts.Fact{}, memberFacts[name]...)
		for _, f := range store.ByKind(facts.KindDependency) {
			if carrierFor(f, c) {
				sources = append(sources, f)
			}
		}
		sortFactsByNameThenFile(sources)
		carried[name] = sources
	}
	ground := newGrounding(store, memberFacts)
	resolve := newResolver(store, components, members, memberFacts, carried, ground)
	refused := map[string]UnreachableRole{}
	for _, u := range unreachableRoles(store, rules, resolve, ground, unasked, unevaluable) {
		if _, seen := refused[u.Rule]; !seen && !u.Partial {
			refused[u.Rule] = u
		}
	}

	for _, r := range rules {
		if _, seen := out[r.id]; seen {
			continue
		}
		if u, ok := unverdictedBeforeVerdict(r, components, unasked, unevaluable, unevaluableProblem, refused); ok {
			out[r.id] = u
			continue
		}
		if u, ok := unverdictedByFinding(r, findings, members); ok {
			out[r.id] = u
			continue
		}
		if u, ok := vacuous(r, members); ok {
			out[r.id] = u
		}
	}
	return out
}

// unverdictedBeforeVerdict answers for the refusals the explainer makes
// before it runs a rule's verdicts, in the order it makes them.
func unverdictedBeforeVerdict(r rule, components map[string]component, unasked, unevaluable map[string]bool, unevaluableProblem map[string]string, refused map[string]UnreachableRole) (Unverdict, bool) {
	if len(r.ambiguous) > 0 {
		a := r.ambiguous[0]
		return Unverdict{UnverdictedAmbiguousComponent, fmt.Sprintf("component %s is declared by %s and not by the rule's own repository", a.name, wordList(a.repos))}, true
	}
	// Read through rule.names, the test namesUnasked silences a rule with, so a
	// rule counted here is exactly a rule the explainer skipped.
	for _, name := range sortedKeys(unasked) {
		if r.names(name) {
			return Unverdict{UnverdictedServiceAbsent, fmt.Sprintf("component %s names service %s, absent from this snapshot", name, components[name].service)}, true
		}
	}
	for _, name := range sortedKeys(unevaluable) {
		if r.names(name) {
			return Unverdict{UnverdictedSelectorUnevaluable, fmt.Sprintf("component %s: %s", name, unevaluableProblem[name])}, true
		}
	}
	if u, ok := refused[r.id]; ok {
		return Unverdict{UnverdictedRoleUnreachable, u.Problem()}, true
	}
	return Unverdict{}, false
}

// unverdictedByFinding reads the refusals and skips a verdict pass decides,
// from the findings that pass wrote. Each prefix is the one the finding is
// titled with in this package.
func unverdictedByFinding(r rule, findings []facts.Insight, members map[string]map[string]bool) (Unverdict, bool) {
	unevaluablePrefix := fmt.Sprintf("Constraint rule %s cannot be evaluated: ", r.id)
	wholeSkips := []string{
		fmt.Sprintf("forbid_reach rule %s skipped: ", r.id),
		fmt.Sprintf("require rule %s skipped: no member of ", r.id),
		fmt.Sprintf("require_edge rule %s skipped: no member of ", r.id),
	}
	visibilitySkips := []string{
		fmt.Sprintf("require_edge rule %s skipped: ", r.id),
		fmt.Sprintf("protocol rule %s skipped: ", r.id),
	}
	for _, in := range findings {
		if in.Source != "" && in.Source != ExplainerName {
			continue
		}
		if rest, ok := strings.CutPrefix(in.Title, unevaluablePrefix); ok {
			return Unverdict{UnverdictedFormUnevaluable, rest}, true
		}
		for _, prefix := range wholeSkips {
			if rest, ok := strings.CutPrefix(in.Title, prefix); ok {
				_, reason, _ := strings.Cut(prefix+rest, " skipped: ")
				return Unverdict{UnverdictedSkipped, reason}, true
			}
		}
		for _, prefix := range visibilitySkips {
			rest, ok := strings.CutPrefix(in.Title, prefix)
			if !ok {
				continue
			}
			subject := r.requireEdge
			if r.protocol != "" {
				subject = r.protocol
			}
			skipped := 0
			for _, ev := range in.Evidence {
				if ev.Symbol != "" {
					skipped++
				}
			}
			if skipped > 0 && skipped >= len(members[subject]) {
				return Unverdict{UnverdictedSkipped, rest}, true
			}
		}
	}
	return Unverdict{}, false
}

// vacuous answers for a rule whose verdicts ran over a membership too empty to
// say anything, in the sense UnverdictedRules states.
func vacuous(r rule, members map[string]map[string]bool) (Unverdict, bool) {
	empty := func(name string) bool { return name != "" && len(members[name]) == 0 }
	matchesNothing := func(name string) (Unverdict, bool) {
		return Unverdict{UnverdictedMatchesNothing, fmt.Sprintf("component %s matches nothing", name)}, true
	}
	switch {
	case r.forbidFact != "", r.cap != "":
		return Unverdict{}, false
	case r.forbidCycles != "":
		parts := append([]string{r.forbidCycles}, r.among...)
		withMembers := 0
		for _, p := range parts {
			if !empty(p) {
				withMembers++
			}
		}
		if withMembers < 2 {
			for _, p := range parts {
				if empty(p) {
					return matchesNothing(p)
				}
			}
		}
		return Unverdict{}, false
	}
	subject := r.subject()
	if empty(subject) {
		return matchesNothing(subject)
	}
	if (r.forbid != "" || r.forbidReach != "") && len(r.toName) == 0 && empty(r.to) {
		return matchesNothing(r.to)
	}
	return Unverdict{}, false
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// subject is the component the rule's form is about.
func (r rule) subject() string {
	for _, name := range []string{
		r.forbid, r.forbidReach, r.allow, r.protect, r.private, r.forbidFact,
		r.cap, r.require, r.requireEdge, r.requireDefines, r.forbidCycles,
		r.independent, r.requireName, r.forbidName, r.protocol, r.guide,
		r.storageStaysHome, r.capRuntime, r.requireConsumer, r.uniqueAcross,
		r.requireGoverned,
	} {
		if name != "" {
			return name
		}
	}
	return ""
}
