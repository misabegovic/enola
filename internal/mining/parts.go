package mining

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/explainers/constraints"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

// GroupingDirectory and GroupingComponent name what a report's edge
// candidates are about: directories two levels deep, or the parts a
// declaration already names.
const (
	GroupingDirectory = "directory"
	GroupingComponent = "component"
)

// DefaultMinResolution is the share of an edge kind's edges that must land on
// something measured before a candidate over that kind is proposed. Below it,
// a pair with no crossing is what the graph cannot see as much as what the
// code does not do.
const DefaultMinResolution = 0.05

// componentVias are the edge kinds component mode reads: the two a declared
// forbid between parts is written over.
var componentVias = []string{"calls", "imports"}

// Support is what a component-mode candidate rests on: how many edges of its
// kind the source part makes, how many of those land on something measured
// (the outgoing count a zero is read against), where they land, and how well
// that kind resolves over every declared part.
type Support struct {
	Part string `json:"part"`
	// Target is the part a forbid names, and Only the parts an allow-only
	// lists, so an artifact reader can compare a candidate with a
	// declaration without parsing its identity.
	Target    string   `json:"target,omitempty"`
	Only      []string `json:"only,omitempty"`
	Via       string   `json:"via"`
	Outgoing  int      `json:"outgoing"`
	Made      int      `json:"made"`
	Inside    int      `json:"inside"`
	Crossing  int      `json:"crossing"`
	Elsewhere int      `json:"elsewhere"`
	// Resolution is Resolved over Made for the kind across every declared
	// part's edges, and BelowFloor whether it is under the report's floor.
	Resolution KindResolution `json:"resolution"`
}

// KindResolution is how one edge kind resolves over the declared parts.
type KindResolution struct {
	Via        string  `json:"via"`
	Resolved   int     `json:"resolved"`
	Made       int     `json:"made"`
	Share      float64 `json:"share"`
	BelowFloor bool    `json:"below_floor"`
}

// DeclaredCandidate is a candidate a declared rule already states.
type DeclaredCandidate struct {
	Candidate
	DeclaredBy []string `json:"declared_by"`
}

// MineParts mines edge candidates over the parts a declaration names: for
// each ordered pair of parts and each edge kind, a forbid when the source
// part's edges of that kind land in the target part rarely or never, and for
// each part an allow-only over the parts its edges do land in. A pair with no
// crossing is proposed, unlike in directory mode, because the source part's
// outgoing count says whether the zero had the chance to be anything else:
// it is printed with every candidate, a candidate under the support floor is
// suppressed, and a kind resolving under the resolution floor makes every
// candidate over it one that cannot be checked here, listed and not proposed.
func MineParts(store *facts.Store, cfg Config) *Report {
	if cfg.MinSupport < 1 {
		cfg.MinSupport = 1
	}
	if cfg.MaxExceptions < 0 {
		cfg.MaxExceptions = 0
	}
	return mineReading(constraints.ReadParts(store, componentVias, cfg.MaxExceptions+1), cfg, store.Count())
}

func mineReading(reading constraints.PartReading, cfg Config, factCount int) *Report {
	report := &Report{Config: cfg, FactCount: factCount, Grouping: GroupingComponent,
		Parts: reading.Parts, UnreadParts: reading.Unread, DeclaredRules: reading.Declared}
	suppressed := map[string]*SuppressedCount{}
	for _, family := range familyOrder {
		suppressed[family] = &SuppressedCount{Family: family}
	}

	layerOf := map[string]constraints.Part{}
	for _, p := range reading.Parts {
		if p.Layer != "" {
			layerOf[p.Name] = p
		}
	}
	resolution := map[string]KindResolution{}
	for _, via := range componentVias {
		kr := KindResolution{Via: via}
		for _, p := range reading.Parts {
			if f, ok := reading.Flow(p.Name, via); ok {
				kr.Resolved += f.Resolved
				kr.Made += f.Made
			}
		}
		kr.Share = ratio(kr.Resolved, kr.Made)
		kr.BelowFloor = kr.Share < cfg.MinResolution
		resolution[via] = kr
		report.Resolution = append(report.Resolution, kr)
	}

	var found []Candidate
	for _, src := range reading.Parts {
		for _, via := range componentVias {
			flow, ok := reading.Flow(src.Name, via)
			if !ok {
				continue
			}
			support := Support{Part: src.Name, Via: via, Outgoing: flow.Resolved, Made: flow.Made,
				Inside: flow.Inside, Crossing: flow.Crossed(), Elsewhere: flow.Elsewhere, Resolution: resolution[via]}
			for _, tgt := range reading.Parts {
				if tgt.Name == src.Name {
					continue
				}
				if c, ok := partForbid(src, tgt, flow, support, cfg, suppressed, layerOf); ok {
					found = append(found, c)
				}
			}
			if c, ok := partAllowOnly(src, flow, support, reading.Parts, cfg, suppressed, layerOf); ok {
				found = append(found, c)
			}
		}
	}

	var proposed []Candidate
	for _, c := range found {
		if by := declaredBy(c, reading.Declared); len(by) > 0 {
			report.Declared = append(report.Declared, DeclaredCandidate{Candidate: c, DeclaredBy: by})
			continue
		}
		proposed = append(proposed, c)
	}
	var rejected []RejectedCandidate
	declared := make([]Candidate, 0, len(report.Declared))
	for _, d := range report.Declared {
		declared = append(declared, d.Candidate)
	}
	proposed, rejected, report.StaleRejections = applyRejections(proposed, cfg.Rejections, declared)
	report.Rejected = rejected
	for _, c := range proposed {
		if c.Support.Resolution.BelowFloor {
			report.Unchecked = append(report.Unchecked, c)
			continue
		}
		report.Candidates = append(report.Candidates, c)
	}
	report.Candidates = finalize(report.Candidates)
	sortByIdentity(report.Unchecked)
	sort.SliceStable(report.Declared, func(i, j int) bool { return report.Declared[i].Identity < report.Declared[j].Identity })
	sort.SliceStable(report.Rejected, func(i, j int) bool { return report.Rejected[i].Identity < report.Rejected[j].Identity })
	for _, family := range familyOrder {
		report.Suppressed = append(report.Suppressed, *suppressed[family])
	}
	return report
}

func sortByIdentity(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Identity < cs[j].Identity })
}

// partComponents lists the declarations a would-be rule needs beside it: none
// for a declared component, which the repository already declares, and one
// for a part read from a layer order, which no component carries yet.
func partComponents(layerOf map[string]constraints.Part, names ...string) []intent.ConstraintComponent {
	var out []intent.ConstraintComponent
	seen := map[string]bool{}
	for _, n := range names {
		p, ok := layerOf[n]
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, intent.ConstraintComponent{Name: p.Name, Service: p.Service, Match: append([]string{}, p.Match...)})
	}
	return out
}

func partForbid(src, tgt constraints.Part, flow constraints.PartFlow, support Support, cfg Config, suppressed map[string]*SuppressedCount, layerOf map[string]constraints.Part) (Candidate, bool) {
	total := flow.Resolved
	crossings := flow.Landed(tgt.Name)
	if total > 0 && ratio(crossings, total) > 1-cfg.MinConfidence {
		return Candidate{}, false
	}
	if crossings > 0 && !cfg.WithCrossings && total >= cfg.MinSupport {
		suppressed[FamilyForbidEdge].Crossed++
		return Candidate{}, false
	}
	exceptions := make([]Exception, 0, len(flow.CrossingsInto(tgt.Name)))
	for _, x := range flow.CrossingsInto(tgt.Name) {
		exceptions = append(exceptions, Exception{Name: x.Source + " -> " + x.Target, File: x.File, Detail: flow.Via + " edge into " + tgt.Name})
	}
	if !admitInto(suppressed, cfg, FamilyForbidEdge, total, crossings) {
		return Candidate{}, false
	}
	support.Target = tgt.Name
	statement := fmt.Sprintf("%s never %s %s: %d of the %d resolved %s %s makes land in %s", src.Name, viaVerb(flow.Via), tgt.Name, crossings, total, flow.Via, src.Name, tgt.Name)
	if crossings > 0 {
		statement = fmt.Sprintf("%s rarely %s %s: %d of the %d resolved %s %s makes land in %s", src.Name, viaVerb(flow.Via), tgt.Name, crossings, total, flow.Via, src.Name, tgt.Name)
	}
	statement = serviced(src.Service, statement)
	rule := intent.ConstraintRule{
		ID:      slug("mined", src.Name, "never", flow.Via, tgt.Name),
		Forbid:  src.Name,
		To:      tgt.Name,
		Via:     flow.Via,
		Mode:    "advisory",
		Because: because(statement, exceptions),
	}
	return Candidate{
		Family:      FamilyForbidEdge,
		Service:     src.Service,
		Identity:    identityKey(FamilyForbidEdge, src.Service, GroupingComponent, src.Name, tgt.Name, flow.Via),
		Statement:   statement,
		Numerator:   total - crossings,
		Denominator: total,
		Confidence:  ratio(total-crossings, total),
		Exceptions:  exceptions,
		Witnesses:   partWitnesses(flow, tgt.Name),
		Components:  partComponents(layerOf, src.Name, tgt.Name),
		Rule:        rule,
		Support:     &support,
	}, true
}

func partAllowOnly(src constraints.Part, flow constraints.PartFlow, support Support, parts []constraints.Part, cfg Config, suppressed map[string]*SuppressedCount, layerOf map[string]constraints.Part) (Candidate, bool) {
	total := flow.Resolved
	landings := append([]constraints.PartLanding{}, flow.Into...)
	sort.SliceStable(landings, func(i, j int) bool {
		if landings[i].Count != landings[j].Count {
			return landings[i].Count > landings[j].Count
		}
		return landings[i].Part < landings[j].Part
	})
	covered := flow.Inside
	var taken []string
	for _, l := range landings {
		if total > 0 && ratio(covered, total) >= cfg.MinConfidence && (cfg.WithCrossings || covered == total) {
			break
		}
		taken = append(taken, l.Part)
		covered += l.Count
	}
	if covered < total && !cfg.WithCrossings && len(taken) <= maxAllowOnlyTargets && total >= cfg.MinSupport {
		suppressed[FamilyAllowOnly].Crossed++
		return Candidate{}, false
	}
	if len(taken) > maxAllowOnlyTargets || (total > 0 && ratio(covered, total) < cfg.MinConfidence) {
		return Candidate{}, false
	}
	sort.Strings(taken)
	takenSet := map[string]bool{}
	for _, t := range taken {
		takenSet[t] = true
	}
	var exceptions []Exception
	for _, l := range flow.Into {
		if takenSet[l.Part] {
			continue
		}
		for _, x := range l.Crossings {
			exceptions = append(exceptions, Exception{Name: x.Source + " -> " + x.Target, File: x.File, Detail: flow.Via + " edge into " + l.Part})
		}
	}
	for _, x := range flow.ElsewhereSample {
		exceptions = append(exceptions, Exception{Name: x.Source + " -> " + x.Target, File: x.File, Detail: flow.Via + " edge into no declared part"})
	}
	sort.Slice(exceptions, func(i, j int) bool {
		if exceptions[i].Name != exceptions[j].Name {
			return exceptions[i].Name < exceptions[j].Name
		}
		return exceptions[i].File < exceptions[j].File
	})
	if !admitInto(suppressed, cfg, FamilyAllowOnly, total, total-covered) {
		return Candidate{}, false
	}
	if len(exceptions) > total-covered {
		exceptions = exceptions[:total-covered]
	}
	only := taken
	if len(only) == 0 {
		only = []string{src.Name}
	}
	support.Only = only
	landed := strings.Join(dedupe(append([]string{src.Name}, taken...)), " and ")
	statement := serviced(src.Service, fmt.Sprintf("%s %s only %s: %d of the %d resolved %s %s makes land there", src.Name, viaVerb(flow.Via), landed, covered, total, flow.Via, src.Name))
	rule := intent.ConstraintRule{
		ID:      slug("mined", src.Name, "only", flow.Via, strings.Join(only, "-")),
		Allow:   src.Name,
		Only:    only,
		Via:     flow.Via,
		Mode:    "advisory",
		Because: because(statement, exceptions),
	}
	return Candidate{
		Family:      FamilyAllowOnly,
		Service:     src.Service,
		Identity:    identityKey(FamilyAllowOnly, src.Service, GroupingComponent, src.Name, flow.Via),
		Statement:   statement,
		Numerator:   covered,
		Denominator: total,
		Confidence:  ratio(covered, total),
		Exceptions:  exceptions,
		Components:  partComponents(layerOf, append([]string{src.Name}, only...)...),
		Rule:        rule,
		Support:     &support,
	}, true
}

func dedupe(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// partWitnesses names the first edges the source part makes that land in
// another part than the forbidden one, so the rule shows what it permits.
func partWitnesses(flow constraints.PartFlow, forbidden string) []Witness {
	var out []Witness
	for _, l := range flow.Into {
		if l.Part == forbidden {
			continue
		}
		for _, x := range l.Crossings {
			if len(out) == maxWitnesses {
				return out
			}
			out = append(out, Witness{Name: x.Source, File: x.File, Target: x.Target})
		}
	}
	return out
}

func admitInto(suppressed map[string]*SuppressedCount, cfg Config, family string, support, exceptions int) bool {
	if support < cfg.MinSupport {
		suppressed[family].BelowSupportFloor++
		return false
	}
	if exceptions > cfg.MaxExceptions {
		suppressed[family].OverExceptionCeiling++
		return false
	}
	return true
}

func viaVerb(via string) string {
	switch via {
	case "calls":
		return "calls"
	case "imports":
		return "imports"
	}
	return "reaches via " + via
}

func serviced(service, text string) string {
	if service == "" {
		return text
	}
	return "[" + service + "] " + text
}

// declaredBy names the declared rules that already state a candidate. A
// forbid of A to B over a kind is stated by a forbid or forbid_reach of A to
// B over that kind, by an allow of A whose only list leaves B out, and, over
// imports, by a layer order placing A below B. An allow-only of A to a list is
// stated by a declared allow of A over the kind whose only list is inside it.
func declaredBy(c Candidate, declared []constraints.DeclaredEdgeRule) []string {
	var out []string
	for _, d := range declared {
		if d.Via != c.Rule.Via {
			continue
		}
		switch c.Family {
		case FamilyForbidEdge:
			switch d.Form {
			case "forbid", "forbid_reach", "layers":
				if d.From == c.Rule.Forbid && d.To == c.Rule.To {
					out = append(out, d.ID)
				}
			case "allow":
				if d.From == c.Rule.Forbid && c.Rule.To != d.From && !contains(d.Only, c.Rule.To) {
					out = append(out, d.ID)
				}
			}
		case FamilyAllowOnly:
			if d.Form != "allow" || d.From != c.Rule.Allow {
				continue
			}
			inside := true
			for _, o := range d.Only {
				if o != d.From && !contains(c.Rule.Only, o) {
					inside = false
					break
				}
			}
			if inside {
				out = append(out, d.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
