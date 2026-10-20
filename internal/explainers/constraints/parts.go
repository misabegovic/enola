package constraints

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// A part is a declared component, or a layer of a declared layer order read as
// one, and the reading below is what each part's edges do as the forbid and
// allow-only verdicts read them: the same membership, the same carriers, the
// same target resolution. A count here and a verdict over the same pair can
// therefore never disagree, which is what lets a mined candidate say "no edge
// crosses" and a declared rule over that pair then lint clean.

// LayerPartPrefix names a part read from a layer order, so it can never take
// the name of a declared component and a would-be rule over it can declare it.
const LayerPartPrefix = "layer-"

// Part is one declared part the reading measured.
type Part struct {
	Name    string   `json:"name"`
	Service string   `json:"service,omitempty"`
	Match   []string `json:"match"`
	Source  string   `json:"source"`
	// Layer is the layer's own name when the part is read from a layer order,
	// and Order its place in that order, outermost first.
	Layer   string `json:"layer,omitempty"`
	Order   int    `json:"order,omitempty"`
	Members int    `json:"members"`
}

// UnreadPart is a declared part the reading could not measure, and why.
type UnreadPart struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// PartCrossing is one edge from a part's source into another part.
type PartCrossing struct {
	Source string `json:"source"`
	File   string `json:"file,omitempty"`
	Target string `json:"target"`
}

// PartLanding counts the edges of one flow landing in one other part, with the
// first crossings in sorted order.
type PartLanding struct {
	Part      string         `json:"part"`
	Count     int            `json:"count"`
	Crossings []PartCrossing `json:"crossings,omitempty"`
}

// PartFlow is what one part's edges of one kind do. Made counts the distinct
// (source, target) edges the part's sources make; Resolved those landing on
// something the snapshot measured; Inside those landing in the part itself;
// Into those landing in each other part (an edge whose target two parts hold
// counts for each); Elsewhere those landing on a measured fact in no part.
type PartFlow struct {
	Part      string        `json:"part"`
	Via       string        `json:"via"`
	Made      int           `json:"made"`
	Resolved  int           `json:"resolved"`
	Inside    int           `json:"inside"`
	Into      []PartLanding `json:"into"`
	Elsewhere int           `json:"elsewhere"`
	// ElsewhereSample is the first edges landing in no part, in walk order.
	ElsewhereSample []PartCrossing `json:"elsewhere_sample,omitempty"`
}

// Landed is the count of the flow's edges landing in the named part.
func (f PartFlow) Landed(part string) int {
	for _, l := range f.Into {
		if l.Part == part {
			return l.Count
		}
	}
	return 0
}

// CrossingsInto lists the first crossings into the named part.
func (f PartFlow) CrossingsInto(part string) []PartCrossing {
	for _, l := range f.Into {
		if l.Part == part {
			return l.Crossings
		}
	}
	return nil
}

// Crossed is the count of the flow's edges landing in any other part.
func (f PartFlow) Crossed() int {
	n := 0
	for _, l := range f.Into {
		n += l.Count
	}
	return n
}

// DeclaredEdgeRule is one declared rule over a pair of parts in the shape a
// mined edge candidate can be compared with: a forbid (or forbid_reach) of one
// part to another, or an allow of one part to a list. A layer order states a
// forbid of every inner layer to every outer one, via imports, which is the
// only edge the layers verdict reads.
type DeclaredEdgeRule struct {
	ID     string   `json:"id"`
	Form   string   `json:"form"`
	From   string   `json:"from"`
	To     string   `json:"to,omitempty"`
	Only   []string `json:"only,omitempty"`
	Via    string   `json:"via"`
	Source string   `json:"source"`
}

// PartReading is the whole reading: the measured parts in name order, the
// parts it could not read, one flow per (part, via) in that order with the
// flows of sources in no part last under NoPart, and the declared edge rules.
type PartReading struct {
	Parts    []Part             `json:"parts"`
	Unread   []UnreadPart       `json:"unread"`
	Flows    []PartFlow         `json:"flows"`
	Declared []DeclaredEdgeRule `json:"declared"`
}

// Flow returns the flow of one part over one kind.
func (r PartReading) Flow(part, via string) (PartFlow, bool) {
	for _, f := range r.Flows {
		if f.Part == part && f.Via == via {
			return f, true
		}
	}
	return PartFlow{}, false
}

// ReadParts reads every declared part's edges of the given kinds off the
// store, keeping at most crossingCap crossings per landing. The store carries
// the compiled declaration as intent facts, as the evaluator's does.
func ReadParts(store *facts.Store, vias []string, crossingCap int) PartReading {
	components, rules := declarations(store)
	out := PartReading{Parts: []Part{}, Unread: []UnreadPart{}, Flows: []PartFlow{}, Declared: []DeclaredEdgeRule{}}

	layers := layerParts(store, components, &out)
	for name, l := range layers {
		components[name] = l.component
	}
	names := make([]string, 0, len(components))
	for n := range components {
		names = append(names, n)
	}
	sort.Strings(names)

	unasked := unaskedComponents(store, components)
	unevaluable := map[string]bool{}
	for _, u := range unevaluableSelectors(store, components, unasked) {
		unevaluable[u.Component] = true
	}

	members := map[string]map[string]bool{}
	memberFacts := map[string][]facts.Fact{}
	var read []string
	for _, name := range names {
		switch {
		case unasked[name]:
			out.Unread = append(out.Unread, UnreadPart{Name: name, Reason: "names a service the snapshot does not carry"})
			continue
		case unevaluable[name]:
			out.Unread = append(out.Unread, UnreadPart{Name: name, Reason: "its selector cannot be evaluated over this snapshot"})
			continue
		}
		members[name], memberFacts[name] = resolveMembership(store, components[name])
		if len(memberFacts[name]) == 0 {
			out.Unread = append(out.Unread, UnreadPart{Name: name, Reason: "its selector admits no measured fact"})
			continue
		}
		read = append(read, name)
	}

	carried := map[string][]facts.Fact{}
	for _, name := range read {
		c := components[name]
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
	resolvable := landings{}
	for _, kind := range append(append([]string{}, memberKinds...), facts.KindService) {
		for _, f := range store.ByKind(kind) {
			resolvable.add(f)
		}
	}

	for _, name := range read {
		c := components[name]
		p := Part{Name: name, Service: c.service, Match: append([]string{}, c.match...), Source: c.source, Members: len(memberFacts[name])}
		if l, ok := layers[name]; ok {
			p.Layer, p.Order = l.layer, l.order
		}
		out.Parts = append(out.Parts, p)
	}

	var none rule
	sourced := map[string]bool{}
	for _, name := range read {
		srcs := resolve.sources(none, name)
		for _, f := range srcs {
			sourced[sourceKey(f)] = true
		}
		for _, via := range vias {
			out.Flows = append(out.Flows, readFlow(name, via, srcs, read, resolve, resolvable, ground, crossingCap))
		}
	}
	var stray []facts.Fact
	for _, kind := range append(append([]string{}, memberKinds...), facts.KindDependency) {
		for _, f := range store.ByKind(kind) {
			if !sourced[sourceKey(f)] {
				stray = append(stray, f)
			}
		}
	}
	sortFactsByNameThenFile(stray)
	for _, via := range vias {
		out.Flows = append(out.Flows, readFlow(NoPart, via, stray, read, resolve, resolvable, ground, crossingCap))
	}

	readSet := map[string]bool{}
	for _, n := range read {
		readSet[n] = true
	}
	for _, r := range rules {
		switch {
		case r.forbid != "" && r.to != "":
			out.Declared = append(out.Declared, DeclaredEdgeRule{ID: r.id, Form: "forbid", From: r.forbid, To: r.to, Via: r.via, Source: r.source})
		case r.forbidReach != "" && r.to != "":
			out.Declared = append(out.Declared, DeclaredEdgeRule{ID: r.id, Form: "forbid_reach", From: r.forbidReach, To: r.to, Via: r.via, Source: r.source})
		case r.allow != "":
			out.Declared = append(out.Declared, DeclaredEdgeRule{ID: r.id, Form: "allow", From: r.allow, Only: append([]string{}, r.only...), Via: r.via, Source: r.source})
		}
	}
	var layerNames []string
	for n := range layers {
		if readSet[n] {
			layerNames = append(layerNames, n)
		}
	}
	sort.Slice(layerNames, func(i, j int) bool { return layers[layerNames[i]].order < layers[layerNames[j]].order })
	for i, inner := range layerNames {
		for _, outer := range layerNames[:i] {
			out.Declared = append(out.Declared, DeclaredEdgeRule{
				ID: "layer order: " + layers[inner].layer + " below " + layers[outer].layer, Form: "layers",
				From: inner, To: outer, Via: facts.RelImports, Source: layers[inner].component.source,
			})
		}
	}
	sort.SliceStable(out.Declared, func(i, j int) bool { return out.Declared[i].ID < out.Declared[j].ID })
	return out
}

func sourceKey(f facts.Fact) string {
	return f.Kind + "\x00" + f.Name + "\x00" + f.File
}

func readFlow(name, via string, srcs []facts.Fact, parts []string, resolve *resolver, resolvable landings, ground *grounding, crossingCap int) PartFlow {
	var none rule
	flow := PartFlow{Part: name, Via: via, Into: []PartLanding{}}
	into := map[string]*PartLanding{}
	seen := map[string]bool{}
	for _, f := range srcs {
		for _, rel := range f.Relations {
			if rel.Kind != via {
				continue
			}
			key := f.Name + "\x00" + rel.Target
			if seen[key] {
				continue
			}
			seen[key] = true
			flow.Made++
			landedAny := false
			for _, other := range parts {
				if _, landed := resolve.target(none, other, rel, f); !landed {
					continue
				}
				landedAny = true
				if other == name {
					flow.Inside++
					continue
				}
				l := into[other]
				if l == nil {
					l = &PartLanding{Part: other}
					into[other] = l
				}
				l.Count++
				if len(l.Crossings) < crossingCap {
					l.Crossings = append(l.Crossings, PartCrossing{Source: f.Name, File: f.File, Target: rel.Target})
				}
			}
			switch {
			case landedAny:
				flow.Resolved++
			case resolvable.measured(rel, f) || ground.resolves(rel, f):
				flow.Resolved++
				flow.Elsewhere++
				if len(flow.ElsewhereSample) < crossingCap {
					flow.ElsewhereSample = append(flow.ElsewhereSample, PartCrossing{Source: f.Name, File: f.File, Target: rel.Target})
				}
			}
		}
	}
	for _, other := range sortedLandingKeys(into) {
		flow.Into = append(flow.Into, *into[other])
	}
	return flow
}

func sortedLandingKeys(m map[string]*PartLanding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// layerParts reads a declared layer order as parts. A layer path is a region
// of the tree, exact (src/lib) or a subtree (src/lib/**), and both select the
// subtree here. A layer whose part name a declared component already holds is
// not read, and says so.
type layerPart struct {
	component component
	layer     string
	order     int
}

func layerParts(store *facts.Store, components map[string]component, out *PartReading) map[string]layerPart {
	layers := map[string]layerPart{}
	for _, f := range store.ByKind(facts.KindIntent) {
		if f.PropString("intent_kind") != "layer" {
			continue
		}
		layer := f.PropString("layer_name")
		if layer == "" {
			continue
		}
		name := LayerPartPrefix + slugPart(layer)
		if _, taken := components[name]; taken {
			out.Unread = append(out.Unread, UnreadPart{Name: name, Reason: fmt.Sprintf("layer %s is not read as a part: a declared component already carries the name", layer)})
			continue
		}
		if _, seen := layers[name]; seen {
			continue
		}
		var match []string
		for _, p := range stringList(f.PropAny("paths")) {
			p = strings.TrimSuffix(strings.TrimSuffix(p, "/**"), "/")
			if p != "" {
				match = append(match, p+"/**")
			}
		}
		sort.Strings(match)
		order, _ := f.PropAny("order").(int)
		if fl, ok := f.PropAny("order").(float64); ok {
			order = int(fl)
		}
		layers[name] = layerPart{component: component{name: name, match: match, source: f.PropString("source")}, layer: layer, order: order}
	}
	return layers
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(t)
	}
	return nil
}

func slugPart(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}
