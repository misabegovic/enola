package intent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConstraintComponent names a set of measured facts by where they live: any
// fact whose file falls under a match pattern (and, when narrowed, whose kind
// or exact name agrees) is a member. Components exist so rules can speak about
// the architecture in the declaration's own vocabulary — "the domain", "the
// adapters" — instead of repeating path lists per rule. Service scopes the
// selector to one repository of a multi-repo snapshot, by its exact repo
// label: members are facts of that repo, and every other narrowing ANDs with
// it. A component may carry a service with no match patterns — that is the
// whole service — where a serviceless component still needs at least one.
type ConstraintComponent struct {
	Name        string   `yaml:"name"`
	Service     string   `yaml:"service"`
	Match       []string `yaml:"match"`
	Kind        string   `yaml:"kind"`
	NamePattern string   `yaml:"name_pattern"`

	// SourceFile is the repo-relative enola/constraints file that declared
	// this component, stamped at load time; empty means declared inline. It
	// is provenance, never YAML input: the compiled fact's File carries it,
	// so a verdict cites the declaring file rather than the merged whole.
	SourceFile string `yaml:"-"`

	Recipe   string `yaml:"-"`
	Instance string `yaml:"-"`
	Role     string `yaml:"-"`
}

// ConstraintRule declares one enforcement statement about components. Exactly
// one of thirteen forms selects what the rule says: Forbid/To (this component must
// not reach that one), ForbidReach/To (this component must not reach that one
// through ANY measured path — the transitive form, walked breadth-first under
// a hard depth cap; Via narrows the walked edge kinds and defaults to every
// rule-via kind), Allow/Only (this component's edges may land only in the
// named components), Protect/Owners (only the named components may reach this
// one — the ownership form, walked from the whole graph rather than from a
// source component), Private/Except (the component's non-exported members may
// be reached only from inside the component or from the Except components —
// the visibility form, verdicted over every rule-via edge kind against the
// extractor-measured exported prop, so it needs no Via of its own), ForbidFact
// (this component's membership must be empty), Cap/MaxMembers (this
// component's membership must not exceed a count), or Require (members of this
// component matching WhenPropContains must satisfy MustPropContain — the
// property form, verdicting what a member fact carries rather than what edges
// it makes; "every storage member whose columns contain company_id must have
// fk_constraints containing company_id->companies"), or RequireDefines/Method
// (every class-kind member symbol must have a measured method symbol of the
// given name — the protocol form; a class whose definition could ride a
// mixin, an included module, or a superclass is out of scope, fail closed,
// because the check cannot see through composition it did not measure), or
// RequireName/Pattern (every member fact's name must match a bounded
// convention pattern — prefix*, *suffix, or an exact name; the naming form
// speaks the same deliberately small dialect philosophy match does, so a
// pattern the evaluator would silently mis-apply cannot be declared), or
// RequireEdge/Via/Direction (every member must have at least one measured edge
// of the Via kind — inbound means some source points at the member, outbound
// means the member points somewhere — optionally scoped by To to a counterpart
// component; the existential form, demanding an edge EXISTS where every other
// edge form forbids one, so an orphaned event or an unconsumed route is a
// breach instead of invisible; a member whose edge visibility the snapshot
// cannot demonstrate for that file kind is skipped with a named count, fail
// closed, never silently compliant and never falsely violated), or
// Protocol/Steps/Via (every member of the protocol component that makes a Via
// edge into step K's members must also make Via edges into every step
// 1..K-1's members — the ordered form. What it verdicts is STRUCTURAL protocol
// conformance: a member referencing a later step's surface without referencing
// every prerequisite step's surface, which a static fact graph can decide. It
// is never a runtime-ordering claim — that step 1 is CALLED before step 2 at
// runtime is unverifiable from a static graph, so the compiled fact carries
// verification: structural and a future runtime provider owns the observed
// level. A member touching no step is a bystander the rule does not bind, and
// a member whose file class cannot demonstrate the Via kind is skipped with a
// named count, fail closed), or
// Guide/Message (steering, not law: advice surfaced to whoever is about to
// edit inside the component — "similar implementations here used X; consider
// it" — with optional Exemplars naming prior art by repo-relative file path
// or fact name; exemplar existence is checked at delivery time against the
// snapshot, never at parse time, because prior art may move without the
// advice going stale).
// Unlike a claim (a measurement expected to hold), a rule carries enforcement
// semantics: a breach is a decided-rule finding, and Because — mandatory — is
// the rationale the resulting finding surfaces, so a violation always says why
// the rule exists, not only that it was broken. Mode softens enforcement:
// ratchet (the default) verdicts at full confidence, advisory reports below
// the check gate's floor so the finding surfaces without failing anything.
// A guidance rule takes only the non-enforcing modes — notify (its default:
// contract/hook channel only, never a finding) or advisory — because
// graduation to law means writing a law form, not hardening this one.
type ConstraintRule struct {
	ID          string   `yaml:"id"`
	Forbid      string   `yaml:"forbid"`
	ForbidReach string   `yaml:"forbid_reach"`
	To          string   `yaml:"to"`
	Allow       string   `yaml:"allow"`
	Only        []string `yaml:"only"`
	Protect     string   `yaml:"protect"`
	Owners      []string `yaml:"owners"`
	Private     string   `yaml:"private"`
	Except      []string `yaml:"except"`
	ForbidFact  string   `yaml:"forbid_fact"`
	Cap         string   `yaml:"cap"`
	MaxMembers  int      `yaml:"max_members"`

	Require          string     `yaml:"require"`
	WhenPropContains *PropMatch `yaml:"when_prop_contains"`
	MustPropContain  *PropMatch `yaml:"must_prop_contain"`

	RequireDefines string `yaml:"require_defines"`
	Method         string `yaml:"method"`

	RequireName string `yaml:"require_name"`
	Pattern     string `yaml:"pattern"`

	RequireEdge string `yaml:"require_edge"`
	Direction   string `yaml:"direction"`

	Protocol string   `yaml:"protocol"`
	Steps    []string `yaml:"steps"`

	Guide     string   `yaml:"guide"`
	Message   string   `yaml:"message"`
	Exemplars []string `yaml:"exemplars"`

	Via     string `yaml:"via"`
	Mode    string `yaml:"mode"`
	Because string `yaml:"because"`

	Exempt []ConstraintExemption `yaml:"exempt"`

	// SourceFile mirrors ConstraintComponent.SourceFile: the repo-relative
	// constraints file that declared this rule, empty when inline.
	SourceFile string `yaml:"-"`

	Recipe   string `yaml:"-"`
	Instance string `yaml:"-"`
}

// PropMatch names one space-separated set-valued prop and a value it must (or
// must not fail to) contain as a whole member. Membership, never substring:
// "columns contains company_id" must not be satisfied by parent_company_id.
type PropMatch struct {
	Prop  string `yaml:"prop"`
	Value string `yaml:"value"`
}

type ConstraintExemption struct {
	Witness string `yaml:"witness" json:"witness"`
	Owner   string `yaml:"owner" json:"owner"`
	Because string `yaml:"because" json:"because"`
	Since   string `yaml:"since" json:"since"`
}

func EncodeExemptions(exempt []ConstraintExemption) string {
	if len(exempt) == 0 {
		return ""
	}
	sorted := append([]ConstraintExemption(nil), exempt...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Witness < sorted[j].Witness })
	data, err := json.Marshal(sorted)
	if err != nil {
		return ""
	}
	return string(data)
}

func DecodeExemptions(encoded string) []ConstraintExemption {
	if encoded == "" {
		return nil
	}
	var out []ConstraintExemption
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		return nil
	}
	return out
}

// AllowedComponentKinds is the closed fact-kind vocabulary a component selector
// may narrow to — the measured kinds the constraints explainer resolves over.
var AllowedComponentKinds = map[string]bool{
	"module": true, "symbol": true, "route": true, "storage": true,
}

func allowedComponentKinds() string {
	return "module, route, storage, symbol"
}

// AllowedRuleVias is the closed edge vocabulary a rule may forbid — relation
// kinds the graph actually carries, so a rule can only forbid something the
// evaluator can see. Implements covers inheritance and mixin inclusion (the
// include/extend/prepend edges the Ruby extractor emits as dependency
// carriers), which is what makes concern rules — who may include what, and
// what a concern may reach — compose from the existing edge forms.
var AllowedRuleVias = map[string]bool{
	"depends_on": true, "imports": true, "calls": true, "implements": true,
}

func allowedRuleVias() string {
	return "calls, depends_on, implements, imports"
}

// AllowedRuleModes is the closed enforcement-mode vocabulary. Ratchet (the
// default) fails the gate on NEW violations; advisory reports below the gate's
// floor and fails nothing; strict fails on every violation, baselined or not —
// the check gate recognizes strict-titled constraint findings and exempts them
// from delta scoping, with the suppression ledger as the only override.
var AllowedRuleModes = map[string]bool{
	"ratchet": true, "advisory": true, "strict": true,
}

func allowedRuleModes() string {
	return "advisory, ratchet, strict"
}

// AllowedEdgeDirections is the require_edge form's closed direction
// vocabulary: inbound demands that some measured edge land on each member,
// outbound demands that each member make one.
var AllowedEdgeDirections = map[string]bool{
	"inbound": true, "outbound": true,
}

func allowedEdgeDirections() string {
	return "inbound, outbound"
}

// AllowedGuidanceModes is the guidance form's own mode vocabulary — the
// non-enforcing subset, with notify as the default. The enforce-class modes
// (ratchet, strict) are rejected on a guidance rule at validation: steering
// that fails a gate is law wearing the wrong form, and graduation means
// writing a law form on the declaring file.
var AllowedGuidanceModes = map[string]bool{
	"notify": true, "advisory": true,
}

func allowedGuidanceModes() string {
	return "advisory, notify"
}

// constraintProblems validates a declaration's components and rules with the
// same rules every other vocabulary gets: named errors, allowed sets spelled
// out. Returned as problem strings so Declaration.Validate folds them into its
// single error alongside the other sections'.
func constraintProblems(components []ConstraintComponent, rules []ConstraintRule) []string {
	var problems []string
	// Locations are indexed per declaring file — inline entries keep the bare
	// components[i]/rules[i] spelling they always had (the merge appends file
	// entries after them, so inline indices are unchanged), while an entry
	// from a constraints file is cited as its file's own i-th entry.
	perSource := map[string]int{}
	at := func(section, sourceFile string) string {
		key := sourceFile + "\x00" + section
		i := perSource[key]
		perSource[key]++
		if sourceFile == "" {
			return fmt.Sprintf("%s[%d]", section, i)
		}
		return fmt.Sprintf("%s: %s[%d]", sourceFile, section, i)
	}
	declaredIn := func(sourceFile string) string {
		if sourceFile == "" {
			return RepoFileName
		}
		return sourceFile
	}
	componentNames := map[string]bool{}
	componentSource := map[string]string{}
	for _, c := range components {
		var loc string
		if c.Recipe != "" {
			loc = fmt.Sprintf("%s: use_recipe %s (recipe %s) role %s", c.SourceFile, c.Instance, c.Recipe, c.Role)
		} else {
			loc = at("components", c.SourceFile)
		}
		if c.Recipe == "" && !validToken(c.Name) {
			problems = append(problems, fmt.Sprintf("%s: name %q must be a lowercase token", loc, c.Name))
		}
		if c.Service != "" && !validToken(c.Service) {
			problems = append(problems, fmt.Sprintf("%s (%s): service %q must be a lowercase token", loc, c.Name, c.Service))
		}
		if len(c.Match) == 0 && c.Service == "" {
			problems = append(problems, fmt.Sprintf("%s (%s): needs at least one match pattern or a service", loc, c.Name))
		}
		for j, m := range c.Match {
			if !validConstraintMatch(m) {
				problems = append(problems, fmt.Sprintf("%s.match[%d]: %q must be an exact path or a prefix/** subtree (no other glob forms)", loc, j, m))
			}
		}
		if c.Kind != "" && !AllowedComponentKinds[c.Kind] {
			problems = append(problems, fmt.Sprintf("%s: kind %q is not a measured fact kind (allowed: %s)", loc, c.Kind, allowedComponentKinds()))
		}
		// A name collision is flagged whenever a constraints file is involved,
		// naming both declaring files: a merged set with two definitions of
		// one component has no single answer for what the name selects.
		if componentNames[c.Name] && (c.SourceFile != "" || componentSource[c.Name] != "") {
			problems = append(problems, fmt.Sprintf("%s: component %q is already declared by %s", loc, c.Name, declaredIn(componentSource[c.Name])))
		}
		if !componentNames[c.Name] {
			componentSource[c.Name] = c.SourceFile
		}
		componentNames[c.Name] = true
	}
	ruleIDs := map[string]bool{}
	ruleSource := map[string]string{}
	for _, r := range rules {
		var loc string
		if r.Recipe != "" {
			loc = fmt.Sprintf("%s: use_recipe %s (recipe %s) rule %s", r.SourceFile, r.Instance, r.Recipe, strings.TrimPrefix(r.ID, r.Instance+"/"))
		} else {
			loc = at("rules", r.SourceFile)
		}
		if r.Recipe == "" && !validToken(r.ID) {
			problems = append(problems, fmt.Sprintf("%s: id %q must be a lowercase token", loc, r.ID))
		} else if ruleIDs[r.ID] {
			if r.SourceFile == "" && ruleSource[r.ID] == "" {
				problems = append(problems, fmt.Sprintf("%s: id %q is declared twice in this declaration", loc, r.ID))
			} else {
				problems = append(problems, fmt.Sprintf("%s: id %q is already declared by %s", loc, r.ID, declaredIn(ruleSource[r.ID])))
			}
		}
		if !ruleIDs[r.ID] {
			ruleSource[r.ID] = r.SourceFile
		}
		ruleIDs[r.ID] = true
		problems = append(problems, ruleFormProblems(loc, r, componentNames, "component")...)
		if r.Guide != "" && len(r.Exempt) > 0 {
			problems = append(problems, fmt.Sprintf("%s (%s): exempt belongs to the law forms — guidance emits no violations to exempt", loc, r.ID))
		}
		problems = append(problems, exemptionProblems(loc, r.ID, r.Exempt)...)
	}
	return problems
}

func ruleFormProblems(loc string, r ConstraintRule, names map[string]bool, noun string) []string {
	var problems []string
	forms := 0
	for _, selector := range []string{r.Forbid, r.ForbidReach, r.Allow, r.Protect, r.Private, r.ForbidFact, r.Cap, r.Require, r.RequireEdge, r.RequireDefines, r.RequireName, r.Protocol, r.Guide} {
		if selector != "" {
			forms++
		}
	}
	if forms != 1 {
		problems = append(problems, fmt.Sprintf("%s (%s): exactly one of forbid, forbid_reach, allow, protect, private, forbid_fact, cap, require, require_edge, require_defines, require_name, protocol, guide selects the rule form (%d given)", loc, r.ID, forms))
	}
	component := func(field, name string) {
		if name != "" && !names[name] {
			problems = append(problems, fmt.Sprintf("%s (%s): %s %q names no declared %s", loc, r.ID, field, name, noun))
		}
	}
	component("forbid", r.Forbid)
	component("forbid_reach", r.ForbidReach)
	component("allow", r.Allow)
	component("protect", r.Protect)
	component("private", r.Private)
	component("forbid_fact", r.ForbidFact)
	component("cap", r.Cap)
	component("require", r.Require)
	component("require_edge", r.RequireEdge)
	component("require_defines", r.RequireDefines)
	component("require_name", r.RequireName)
	component("protocol", r.Protocol)
	component("guide", r.Guide)
	edgeForm := r.Forbid != "" || r.Allow != "" || r.Protect != ""
	switch {
	case r.Forbid != "":
		if r.To == "" {
			problems = append(problems, fmt.Sprintf("%s (%s): forbid needs a to component", loc, r.ID))
		}
		component("to", r.To)
	case r.ForbidReach != "":
		if r.To == "" {
			problems = append(problems, fmt.Sprintf("%s (%s): forbid_reach needs a to component", loc, r.ID))
		}
		component("to", r.To)
	case r.Allow != "":
		if len(r.Only) == 0 {
			problems = append(problems, fmt.Sprintf("%s (%s): allow needs at least one only component", loc, r.ID))
		}
		for _, name := range r.Only {
			component("only", name)
		}
	case r.Protect != "":
		if len(r.Owners) == 0 {
			problems = append(problems, fmt.Sprintf("%s (%s): protect needs at least one owners component", loc, r.ID))
		}
		for _, name := range r.Owners {
			component("owners", name)
		}
	case r.Private != "":
		for _, name := range r.Except {
			component("except", name)
		}
	case r.Cap != "":
		if r.MaxMembers < 1 {
			problems = append(problems, fmt.Sprintf("%s (%s): cap needs max_members of at least 1 — an empty surface is forbid_fact's form", loc, r.ID))
		}
	case r.RequireEdge != "":
		if !AllowedRuleVias[r.Via] {
			problems = append(problems, fmt.Sprintf("%s (%s): via %q is not a rule edge kind (allowed: %s)", loc, r.ID, r.Via, allowedRuleVias()))
		}
		if !AllowedEdgeDirections[r.Direction] {
			problems = append(problems, fmt.Sprintf("%s (%s): require_edge needs a direction (allowed: %s) — inbound demands a measured edge land on each member, outbound demands each member make one", loc, r.ID, allowedEdgeDirections()))
		}
		component("to", r.To)
	case r.RequireDefines != "":
		if r.Method == "" || strings.ContainsAny(r.Method, " \t") {
			problems = append(problems, fmt.Sprintf("%s (%s): require_defines needs a method — one whitespace-free method name the class members must define", loc, r.ID))
		}
	case r.RequireName != "":
		if !validNamePattern(r.Pattern) {
			problems = append(problems, fmt.Sprintf("%s (%s): require_name needs a pattern that is an exact name, a prefix*, or a *suffix (no other pattern forms)", loc, r.ID))
		}
	case r.Protocol != "":
		if !AllowedRuleVias[r.Via] {
			problems = append(problems, fmt.Sprintf("%s (%s): via %q is not a rule edge kind (allowed: %s)", loc, r.ID, r.Via, allowedRuleVias()))
		}
		if len(r.Steps) < 2 {
			problems = append(problems, fmt.Sprintf("%s (%s): protocol needs at least 2 steps — a single step declares no order to conform to", loc, r.ID))
		}
		seenStep := map[string]bool{}
		for _, step := range r.Steps {
			component("steps", step)
			if seenStep[step] {
				problems = append(problems, fmt.Sprintf("%s (%s): step %q appears twice in the declared order — each step holds one position", loc, r.ID, step))
			}
			seenStep[step] = true
		}
	case r.Require != "":
		if r.MustPropContain == nil || r.MustPropContain.Prop == "" || r.MustPropContain.Value == "" {
			problems = append(problems, fmt.Sprintf("%s (%s): require needs must_prop_contain with prop and value — a requirement that demands nothing enforces nothing", loc, r.ID))
		}
		if r.WhenPropContains != nil && (r.WhenPropContains.Prop == "" || r.WhenPropContains.Value == "") {
			problems = append(problems, fmt.Sprintf("%s (%s): when_prop_contains needs both prop and value; omit it to require of every member", loc, r.ID))
		}
	case r.Guide != "":
		if r.Message == "" {
			problems = append(problems, fmt.Sprintf("%s (%s): guide needs a message — the advice is what a guidance rule delivers", loc, r.ID))
		}
		for j, ex := range r.Exemplars {
			if ex == "" || strings.ContainsAny(ex, " \t") {
				problems = append(problems, fmt.Sprintf("%s (%s): exemplars[%d] %q must be a non-empty whitespace-free file path or fact name", loc, r.ID, j, ex))
			}
		}
		if r.Mode != "" && !AllowedGuidanceModes[r.Mode] {
			problems = append(problems, fmt.Sprintf("%s (%s): mode %q is not a guidance mode (allowed: %s) — guidance steers, never enforces; graduating it to law means writing a law form", loc, r.ID, r.Mode, allowedGuidanceModes()))
		}
	}
	if edgeForm && !AllowedRuleVias[r.Via] {
		problems = append(problems, fmt.Sprintf("%s (%s): via %q is not a rule edge kind (allowed: %s)", loc, r.ID, r.Via, allowedRuleVias()))
	}
	// Forbid_reach walks every rule-via edge kind by default, so its via is
	// optional — but a declared one must still come from the vocabulary.
	if r.ForbidReach != "" && r.Via != "" && !AllowedRuleVias[r.Via] {
		problems = append(problems, fmt.Sprintf("%s (%s): via %q is not a rule edge kind (allowed: %s)", loc, r.ID, r.Via, allowedRuleVias()))
	}
	if !edgeForm && r.ForbidReach == "" && r.RequireEdge == "" && r.Protocol == "" && r.Via != "" {
		problems = append(problems, fmt.Sprintf("%s (%s): via belongs to the edge forms (forbid, forbid_reach, allow, require_edge, protocol), not this one", loc, r.ID))
	}
	if r.Forbid == "" && r.ForbidReach == "" && r.RequireEdge == "" && r.To != "" {
		problems = append(problems, fmt.Sprintf("%s (%s): to belongs to the forbid, forbid_reach and require_edge forms", loc, r.ID))
	}
	if r.RequireEdge == "" && r.Direction != "" {
		problems = append(problems, fmt.Sprintf("%s (%s): direction belongs to the require_edge form", loc, r.ID))
	}
	if r.Protocol == "" && len(r.Steps) > 0 {
		problems = append(problems, fmt.Sprintf("%s (%s): steps belongs to the protocol form", loc, r.ID))
	}
	if r.Allow == "" && len(r.Only) > 0 {
		problems = append(problems, fmt.Sprintf("%s (%s): only belongs to the allow form", loc, r.ID))
	}
	if r.Protect == "" && len(r.Owners) > 0 {
		problems = append(problems, fmt.Sprintf("%s (%s): owners belongs to the protect form", loc, r.ID))
	}
	if r.Private == "" && len(r.Except) > 0 {
		problems = append(problems, fmt.Sprintf("%s (%s): except belongs to the private form", loc, r.ID))
	}
	if r.Cap == "" && r.MaxMembers != 0 {
		problems = append(problems, fmt.Sprintf("%s (%s): max_members belongs to the cap form", loc, r.ID))
	}
	if r.Require == "" && (r.WhenPropContains != nil || r.MustPropContain != nil) {
		problems = append(problems, fmt.Sprintf("%s (%s): when_prop_contains/must_prop_contain belong to the require form", loc, r.ID))
	}
	if r.RequireDefines == "" && r.Method != "" {
		problems = append(problems, fmt.Sprintf("%s (%s): method belongs to the require_defines form", loc, r.ID))
	}
	if r.RequireName == "" && r.Pattern != "" {
		problems = append(problems, fmt.Sprintf("%s (%s): pattern belongs to the require_name form", loc, r.ID))
	}
	if r.Guide == "" && (r.Message != "" || len(r.Exemplars) > 0) {
		problems = append(problems, fmt.Sprintf("%s (%s): message/exemplars belong to the guide form", loc, r.ID))
	}
	if r.Guide == "" && r.Mode != "" && !AllowedRuleModes[r.Mode] {
		problems = append(problems, fmt.Sprintf("%s (%s): mode %q is not an enforcement mode (allowed: %s)", loc, r.ID, r.Mode, allowedRuleModes()))
	}
	if r.Because == "" {
		problems = append(problems, fmt.Sprintf("%s (%s): needs a because — a rule with no stated rationale cannot surface one in its findings", loc, r.ID))
	}
	return problems
}

func exemptionProblems(loc, ruleID string, exempt []ConstraintExemption) []string {
	var problems []string
	seenWitness := map[string]bool{}
	for j, ex := range exempt {
		entry := fmt.Sprintf("%s (%s): exempt[%d]", loc, ruleID, j)
		switch {
		case strings.TrimSpace(ex.Witness) == "":
			problems = append(problems, fmt.Sprintf("%s: missing witness — the exact violation identity the rule would otherwise report, matched exactly (no glob forms)", entry))
		case seenWitness[ex.Witness]:
			problems = append(problems, fmt.Sprintf("%s: witness %q is already exempted on this rule", entry, ex.Witness))
		}
		seenWitness[ex.Witness] = true
		if strings.TrimSpace(ex.Owner) == "" {
			problems = append(problems, fmt.Sprintf("%s: missing owner — an exemption is a decision someone signs", entry))
		}
		if strings.TrimSpace(ex.Because) == "" {
			problems = append(problems, fmt.Sprintf("%s: missing because — an exemption with no recorded reason is how a violation goes permanently silent", entry))
		}
		if _, err := time.Parse("2006-01-02", ex.Since); err != nil {
			problems = append(problems, fmt.Sprintf("%s: since %q must be YYYY-MM-DD", entry, ex.Since))
		}
	}
	return problems
}

// validNamePattern enforces the naming form's bounded dialect: an exact name,
// a prefix followed by one trailing *, or one leading * followed by a suffix —
// never a general glob or regex, for the same reason match patterns are
// bounded: a convention the evaluator would silently mis-apply must be
// impossible to declare. The literal part must be non-empty and carry no
// pattern metacharacters.
func validNamePattern(pattern string) bool {
	literal := pattern
	switch {
	case strings.HasPrefix(pattern, "*"):
		literal = pattern[1:]
	case strings.HasSuffix(pattern, "*"):
		literal = pattern[:len(pattern)-1]
	}
	if literal == "" {
		return false
	}
	return !strings.ContainsAny(literal, "*?[]{}")
}

// validConstraintMatch enforces the same bounded glob dialect declared layers
// match with (see layers' matchDeclaredLayerPath): an exact repo-relative path,
// or a `prefix/**` subtree — nothing more. Any other glob metacharacter is
// rejected at parse time, so a selector the evaluator would silently fail to
// match is an error the declaration's author sees instead.
func validConstraintMatch(pattern string) bool {
	prefix, _ := strings.CutSuffix(pattern, "/**")
	if prefix == "" {
		return false
	}
	return !strings.ContainsAny(prefix, "*?[]{}")
}
