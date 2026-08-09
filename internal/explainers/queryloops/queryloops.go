// Package queryloops reports a database query issued once per iteration of a
// data-sized loop.
//
// It reports the shape it can prove and deliberately not the one everybody
// means by "N+1". The pitched detector was `record.association` inside a loop,
// and the funnel on teamtailor ends at zero: 1,698 association-name reads in
// unbounded loops, 62 once bare `self.assoc` reads are dropped (Rails memoises
// those), 8 once thread-local receivers like `Current.company` are dropped,
// 7 after eager-loading — and all seven false. `Requisition#send_pusher_event!`
// reads `channel.trigger` where `channel` is a `PusherChannel`, matched only
// because `belongs_to :trigger` exists on an unrelated model. The graph has no
// receiver type inference for Ruby, so `candidate.posts` and `client.post` are
// the same string to it.
//
// A class-level query has no such problem: the receiver IS the type. When
// `AccessLevel.find_by` appears inside an unbounded loop, `AccessLevel` is a
// model this graph already knows and `find_by` is a query — nothing is
// inferred. 97 of these on teamtailor.
//
// It also needs no eager-load suppression, and that is a property of the shape
// rather than a shortcut: `includes` cannot help a class-level `find_by`.
package queryloops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

type Explainer struct{}

func New() *Explainer { return &Explainer{} }

func (e *Explainer) Name() string { return "query-loops" }

// queryMethods are ActiveRecord class methods that reach the database. A class
// method that does not is not a finding — `AccessLevel.human_name` is free.
var queryMethods = map[string]bool{
	"find": true, "find_by": true, "find_by!": true, "find_or_create_by": true,
	"find_or_create_by!": true, "find_or_initialize_by": true, "where": true,
	"first": true, "last": true, "count": true, "exists?": true, "pluck": true,
	"sum": true, "create": true, "create!": true, "update": true, "update!": true,
	"destroy": true, "destroy_all": true, "save": true, "save!": true,
	"order": true, "find_each": true, "take": true, "average": true, "minimum": true,
	"maximum": true, "delete_all": true, "upsert": true, "insert": true,
}

type finding struct {
	symbol string
	file   string
	repo   string
	call   string
	depth  int
}

func (e *Explainer) Explain(ctx context.Context, store *facts.Store) ([]facts.Insight, error) {
	models := modelClasses(store)
	if len(models) == 0 {
		return nil, nil
	}

	var found []finding
	for _, fact := range store.ByKind(facts.KindSymbol) {
		if lang, _ := fact.Props["language"].(string); lang != "ruby" {
			continue
		}
		depth := propInt(fact.Props["loop_depth"])
		for _, call := range propStrings(fact.Props["calls_in_loop"]) {
			receiver, method, ok := strings.Cut(call, ".")
			if !ok || !queryMethods[method] {
				continue
			}
			if !models[demodulize(receiver)] {
				continue
			}
			found = append(found, finding{
				symbol: fact.Name, file: fact.File, repo: fact.Repo,
				call: call, depth: depth,
			})
		}
	}
	if len(found) == 0 {
		return nil, nil
	}

	// Deeper loops first: a query at depth 2 runs a product of two collections,
	// which is a different order of problem from one at depth 1.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].depth != found[j].depth {
			return found[i].depth > found[j].depth
		}
		return found[i].symbol < found[j].symbol
	})

	out := make([]facts.Insight, 0, len(found))
	for _, f := range found {
		out = append(out, facts.Insight{
			Title: fmt.Sprintf("%s issues %s once per iteration", f.symbol, f.call),
			Description: fmt.Sprintf(
				"%s calls %s inside a loop nested %d deep. The receiver is a model this "+
					"graph knows and the method reaches the database, so the query count "+
					"grows with the collection. Eager loading cannot help a class-level "+
					"query — the fix is to read the set once outside the loop.",
				f.symbol, f.call, f.depth),
			// Below 1.0 and deliberately so: the loop is measured, the receiver is
			// measured, and whether the loop is hot is not. This is a candidate to
			// verify against a query count, which is the one oracle available here.
			Confidence: 0.8,
			Evidence: []facts.Evidence{{
				File:   f.file,
				Symbol: f.symbol,
				Detail: fmt.Sprintf("%s at loop depth %d", f.call, f.depth),
			}},
			Actions: []string{
				"read the records once before the loop and index them in memory",
				"confirm with a query-count test that fails against the current code",
			},
		})
	}
	return out, nil
}

// modelClasses is the set of class names this graph knows to be models, keyed
// without namespace. Being able to answer that question is the entire reason
// this explainer works where the association-read one did not.
func modelClasses(store *facts.Store) map[string]bool {
	models := map[string]bool{}
	for _, fact := range store.ByKind(facts.KindStorage) {
		if kind, _ := fact.Props["storage_kind"].(string); kind != "model" {
			continue
		}
		models[demodulize(fact.Name)] = true
	}
	return models
}

func demodulize(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}

func propStrings(v any) []string {
	switch value := v.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func propInt(v any) int {
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return 0
}
