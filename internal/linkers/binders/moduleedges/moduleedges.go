// Package moduleedges gives the module layer the edges its symbols imply.
//
// Every module explainer reads one graph, built from dependency facts whose
// imports relations name another module. A language with import statements
// fills that graph from its extractor; Ruby has no such statement, so its
// module facts stood isolated and the readings that walk them (cycles,
// depth, coupling, layers) answered nothing about the language most of this
// estate is written in. The measurement was blunter than the language
// framing: module facts carry no relations in ANY language in the cluster,
// so the reading was blind wherever an extractor did not emit dependency
// facts of its own.
//
// This derives the missing edges from what the graph already resolved. For
// each symbol, every call, dependency, instantiation and injection whose
// target names a symbol the store holds contributes one edge from the module
// declaring the source to the module declaring the target. A target that resolves to
// nothing contributes nothing and is already counted by the extractor's call
// coverage: an unresolved bare name is exactly the case where a rolled-up
// edge would connect two directories on the strength of a shared method
// name.
//
// The edge carries the number of symbol edges behind it, so a reader can
// tell one stray call from a hundred, and its own coupling kind, so the
// readings that already narrow the graph by kind can decide about it. A
// pair an extractor already connected is left alone: this adds the edges
// nobody emitted, it does not restate the ones somebody did.
package moduleedges

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/explainers/common"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/plugin"
)

// WeightProp is the number of resolved symbol edges a derived module edge
// stands on.
const WeightProp = "symbol_edges"

// DerivedProp names what the edge was derived from, so a reader can tell it
// from an import an extractor read.
const DerivedProp = "derived"

const derivedFromSymbols = "symbol-rollup"

// rolledRelations are the symbol relations that mean "this code needs that code".
//
// `injects` belongs here for the same reason the others do, and its absence cost
// the most on exactly the codebases this binder was written for. A constructor
// parameter IS how a dependency is declared under a DI container — in Spring, in
// ASP.NET Core, in Angular — and there is frequently no call, instantiation or
// import edge beside it to carry the pair: the container does the constructing, and
// what the file imports is a type name that resolves to an interface or a barrel
// rather than to the module the collaborator lives in. Measured across the corpus,
// rolling injection up adds module pairs nothing else connects: an Angular
// storefront library goes from 3,895 derived edges to 6,128, a Java monolith from
// 1,407 to 1,700, and an ASP.NET Core media server from 68 to 97. Seven extractors
// emit the relation, so this is the general case rather than one language's — the
// oldest fixture in the tree gains the component-to-service and route-to-service
// edges an application of that framework is mostly made of.
//
// It moves values rather than verdicts: on the corpus no explainer's finding COUNT
// changes, while what the findings say does — one storefront's deepest dependency
// chain is 78 rather than 77. That is the expected shape for an edge that was
// always there and simply had no fact.
//
// Nothing else about the derivation changes: an injected type that resolves to no
// known symbol still contributes nothing, a pair an extractor already connected is
// still left alone, and a target in another repository is still the cross-repo
// linker's business.
var rolledRelations = map[string]bool{
	facts.RelCalls:        true,
	facts.RelDependsOn:    true,
	facts.RelInstantiates: true,
	facts.RelInjects:      true,
}

type Binder struct{}

func New() *Binder { return &Binder{} }

func (b *Binder) Name() string { return "module-edges" }

func (b *Binder) Stage() plugin.BindStage { return plugin.StagePostLink }

type edge struct {
	source, target string
	repo           string
}

func (b *Binder) Bind(_ context.Context, store *facts.Store) error {
	// Recomputed from scratch each run, prior derived edges dropped first, the
	// same idempotence the cross-repo linker keeps for its synthetics. Without
	// the drop, a store carrying earlier derivations (an append turn, a
	// restored baseline replay) re-derives beside them: the existing-pair
	// check cannot see a prior derived edge, because that edge's File is the
	// module itself and the candidate walk starts from the file's DIRNAME, so
	// the prior registers under its parent module and the pair looks new. The
	// measured cost was 3,266 phantom added facts on an unchanged member's
	// replay against the estate's pin.
	store.RemoveWhere(func(f facts.Fact) bool {
		return f.Props != nil && f.Props[DerivedProp] == derivedFromSymbols
	})
	production, test := moduleNames(store)
	if len(production) == 0 {
		return nil
	}

	// Keyed by (repo, name), never by name alone: 82 of this cluster's symbol
	// names are carried by more than one repository, and a name-keyed map
	// resolves each to whichever repo happened to be read last — so the same
	// union produced different rollups depending on fact order, and a
	// baseline replay (whose re-read member sits at the store's tail) diffed
	// against its own pin. Within one repo a name can still collide across
	// files; the smallest module wins, an order-free tie-break rather than a
	// last-write win. nameRepos keeps the count of repos carrying each name,
	// so a target that resolves only outside its caller's repo is still
	// counted as the cross-repo landing it is rather than as unresolved.
	type key struct{ repo, name string }
	type placed struct{ module string }
	symbolModule := map[key]placed{}
	nameRepos := map[string]int{}
	seenNameInRepo := map[key]bool{}
	for _, f := range store.ByKind(facts.KindSymbol) {
		if f.File == "" {
			continue
		}
		module := resolve(f, production, test)
		if module == "" || !production[module] {
			continue
		}
		k := key{repo: f.Repo, name: f.Name}
		if !seenNameInRepo[k] {
			seenNameInRepo[k] = true
			nameRepos[f.Name]++
		}
		if prior, ok := symbolModule[k]; ok && prior.module <= module {
			continue
		}
		symbolModule[k] = placed{module: module}
	}
	if len(symbolModule) == 0 {
		return nil
	}

	weights := map[edge]int{}
	unresolved, crossRepo := 0, 0
	for _, f := range store.ByKind(facts.KindSymbol) {
		source, ok := symbolModule[key{repo: f.Repo, name: f.Name}]
		if !ok {
			continue
		}
		for _, r := range f.Relations {
			if !rolledRelations[r.Kind] {
				continue
			}
			// The target resolves in the caller's own repository. A directory
			// name is repo-relative, so a rollup that crossed repositories on
			// the strength of a shared name would merge two graphs into one
			// and manufacture dependencies nobody wrote; a dependency that
			// genuinely crosses a repository is the cross-repo linker's,
			// derived from a measured seam rather than from two directories
			// sharing a name.
			target, known := symbolModule[key{repo: f.Repo, name: r.Target}]
			if !known {
				if nameRepos[r.Target] > 0 {
					crossRepo++
				} else {
					unresolved++
				}
				continue
			}
			if target.module == source.module {
				continue
			}
			weights[edge{source: source.module, target: target.module, repo: f.Repo}]++
		}
	}
	if len(weights) == 0 {
		return nil
	}

	existing := extractorEdges(store, production, test)
	derived := make([]facts.Fact, 0, len(weights))
	skipped := 0
	for e, weight := range weights {
		if existing[[2]string{e.source, e.target}] {
			skipped++
			continue
		}
		derived = append(derived, facts.Fact{
			Kind: facts.KindDependency,
			Name: fmt.Sprintf("module-edge: %s -> %s", e.source, e.target),
			File: e.source,
			Repo: e.repo,
			Props: map[string]any{
				facts.PropCouplingKind: facts.CouplingSymbolRollup,
				WeightProp:             weight,
				DerivedProp:            derivedFromSymbols,
			},
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: e.target}},
		})
	}
	if len(derived) == 0 {
		return nil
	}
	sort.Slice(derived, func(i, j int) bool { return derived[i].Name < derived[j].Name })
	store.Add(derived...)
	log.Printf("[module-edges] derived %d module edge(s) from resolved symbol edges; %d pair(s) an extractor already connected, %d symbol edge(s) naming no known symbol, %d landing in another repository",
		len(derived), skipped, unresolved, crossRepo)
	return nil
}

func moduleNames(store *facts.Store) (production, test map[string]bool) {
	production, test = map[string]bool{}, map[string]bool{}
	for _, m := range store.ByKind(facts.KindModule) {
		if common.IsTestModule(m) {
			test[m.Name] = true
			continue
		}
		production[m.Name] = true
	}
	return production, test
}

// resolve names the module a fact's file belongs to, through the same
// candidate walk the module graph builder uses, so an edge this binder
// derives and an edge the builder attributes cannot land on different nodes.
func resolve(f facts.Fact, production, test map[string]bool) string {
	for _, candidate := range common.ModuleDirCandidates(f) {
		if production[candidate] || test[candidate] {
			return candidate
		}
	}
	for _, candidate := range common.ModuleDirCandidates(f) {
		for cur := candidate; ; {
			if production[cur] || test[cur] {
				return cur
			}
			i := strings.LastIndex(cur, "/")
			if i < 0 {
				break
			}
			cur = cur[:i]
		}
	}
	return ""
}

// extractorEdges is the set of module pairs some extractor already connected,
// so a derived edge never restates one that was read from source.
func extractorEdges(store *facts.Store, production, test map[string]bool) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, dep := range store.ByKind(facts.KindDependency) {
		source := resolve(dep, production, test)
		if source == "" {
			continue
		}
		for _, r := range dep.Relations {
			if r.Kind == facts.RelImports {
				out[[2]string{source, r.Target}] = true
			}
		}
	}
	return out
}
