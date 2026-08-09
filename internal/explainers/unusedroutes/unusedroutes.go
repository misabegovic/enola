// Package unusedroutes provides an explainer that summarizes server routes which
// no loaded client repo calls — endpoints the engine's cross-repo link pass
// flagged with the "unmatched_by_clients" prop — into "unused endpoint" candidate
// insights.
//
// The flag is computed structurally from the loaded snapshot only, so this
// explainer is deliberately careful to frame results as *candidates*: a route is
// listed because no client in the snapshot calls it, which is not the same as
// dead. Consumers outside the snapshot — admin/ops scripts, cron jobs, webhooks,
// third-party API clients, mobile deep links — do not appear here, so each
// candidate must be verified against those before removal.
package unusedroutes

import (
	"context"
	"fmt"
	"sort"

	"github.com/enola-labs/enola/internal/facts"
)

// incompleteClientsShare is the proportion of unmatched routes above which the
// finding is about the client set rather than the endpoints.
const incompleteClientsShare = 0.8

// maxActionableCandidates is how many candidates a person can plausibly triage.
// Past it the output stops being a list and becomes a census: teamtailor reports
// 3,392 unmatched at 77% — under the share threshold and still unreadable as
// "endpoints to consider removing". A finding nobody can act on is telemetry.
const maxActionableCandidates = 200

// maxSamples bounds how many route names an insight lists inline.
const maxSamples = 25

// Explainer emits one insight per repo whose server routes include endpoints that
// no loaded client calls.
type Explainer struct{}

// New creates a new Explainer.
func New() *Explainer { return &Explainer{} }

func (e *Explainer) Name() string { return "unused-routes" }

// Explain groups client-unmatched server routes by repo and emits one insight per
// repo, each carrying the mandatory out-of-snapshot caveat. It returns nothing for
// single-repo snapshots (no service nodes, so the cross-repo flagging never ran).
func (e *Explainer) Explain(ctx context.Context, store *facts.Store) ([]facts.Insight, error) {
	if len(store.ByKind(facts.KindService)) == 0 {
		return nil, nil
	}

	// Served counts every server route per repo, so an unmatched count can be
	// read as a proportion. Without it a finding says "3,392 route(s) have no
	// caller" and reads as 3,392 dead endpoints, when what it actually means is
	// that the client set is incomplete.
	served := map[string]int{}
	for _, f := range store.ByKind(facts.KindRoute) {
		if f.Props == nil {
			continue
		}
		if role, _ := f.Props["role"].(string); role == "client" {
			continue
		}
		repo := f.Repo
		if repo == "" {
			repo = "(unlabeled)"
		}
		served[repo]++
	}

	byRepo := map[string][]route{}
	for _, f := range store.ByKind(facts.KindRoute) {
		if f.Props == nil || f.Props["unmatched_by_clients"] != true {
			continue
		}
		repo := f.Repo
		if repo == "" {
			repo = "(unlabeled)"
		}
		byRepo[repo] = append(byRepo[repo], route{name: f.Name, file: f.File, label: routeLabel(f)})
	}
	if len(byRepo) == 0 {
		return nil, nil
	}

	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)

	var insights []facts.Insight
	for _, repo := range repos {
		routes := byRepo[repo]
		sort.Slice(routes, func(i, j int) bool { return routes[i].label < routes[j].label })

		total := served[repo]
		share := 0.0
		if total > 0 {
			share = float64(len(routes)) / float64(total)
		}

		// When nearly everything is unmatched, the snapshot is telling you about
		// its own client set rather than about the endpoints. Teamtailor reports
		// 3,392 of 3,732 that way — its real callers are a browser, an external
		// integrator and a mobile app, none of which is a repository. Presenting
		// that as a list of dead-endpoint candidates is how a finding gets ignored
		// wholesale, and the ignoring is deserved.
		if share >= incompleteClientsShare || len(routes) > maxActionableCandidates {
			insights = append(insights, facts.Insight{
				Title: fmt.Sprintf("Client coverage for %s is too thin to name unused endpoints (%d of %d unmatched)",
					repo, len(routes), total),
				Description: fmt.Sprintf(
					"%d of %s's %d server route(s) — %.0f%% — matched no client call site in any loaded repo. "+
						"At that proportion this describes the snapshot's client set, not the endpoints: an API "+
						"whose callers are browsers, third-party integrators or apps outside the estate cannot "+
						"be assessed this way. Add the missing client repositories and regenerate before reading "+
						"any of these as dead.",
					len(routes), repo, total, share*100),
				Confidence: 0.9,
				Actions: []string{
					"Add the repositories that call this API and regenerate; the list is not meaningful until then",
				},
			})
			continue
		}

		insights = append(insights, facts.Insight{
			Title: fmt.Sprintf("Unused endpoint candidates: %d of %d route(s) in %s have no caller among loaded clients",
				len(routes), total, repo),
			Description: fmt.Sprintf("%d of %s's %d server route(s) matched no client call site in any loaded repo. "+
				"These are candidates unused by the loaded clients ONLY — the snapshot cannot see consumers it "+
				"does not contain (admin/ops scripts, cron jobs, webhooks, third-party API clients, mobile deep "+
				"links). Verify each against those before removing the endpoint. Samples: %s.",
				len(routes), repo, total, sampleList(routes)),
			Confidence: 0.6,
			Evidence:   evidenceFor(routes),
			Actions: []string{
				"Verify each candidate against consumers not in the snapshot before removing the endpoint",
				"Append any missing client repo and regenerate to shrink the list to truly-unused routes",
			},
		})
	}
	return insights, nil
}

// route carries the three things an unmatched route contributes downstream: the fact
// NAME, which is the only string the snapshot diff can match; the FILE, which is what
// scopes an insight to a repo in a multi-repo snapshot; and the human-readable LABEL
// used in the title and description.
//
// Keeping the name separate from the label is the point. The label is "GET /orders"
// while the fact is named "/orders", so citing the label as evidence matched nothing:
// the diff could never attribute a newly-unused route to the change that caused it,
// and query_insights(repo=…) — which scopes by evidence file or path — returned
// nothing at all for the one explainer that exists only in multi-repo snapshots.
type route struct {
	name  string
	file  string
	label string
}

// routeLabel renders a route fact as "METHOD /path" when a method prop is present,
// else just its name.
func routeLabel(f facts.Fact) string {
	if m, ok := f.Props["method"].(string); ok && m != "" {
		return m + " " + f.Name
	}
	return f.Name
}

// sampleList renders up to maxSamples route labels, noting how many were elided.
func sampleList(routes []route) string {
	labels := make([]string, 0, len(routes))
	for _, r := range routes {
		labels = append(labels, r.label)
	}
	if len(labels) > maxSamples {
		return fmt.Sprintf("%v (+%d more)", labels[:maxSamples], len(labels)-maxSamples)
	}
	return fmt.Sprintf("%v", labels)
}

// evidenceFor attaches up to maxSamples routes as insight evidence, keyed on the route
// fact's name so the delta can attribute it, with the method kept in the detail so two
// verbs on one path stay distinguishable.
func evidenceFor(routes []route) []facts.Evidence {
	n := len(routes)
	if n > maxSamples {
		n = maxSamples
	}
	out := make([]facts.Evidence, 0, n)
	for _, r := range routes[:n] {
		detail := "no loaded client calls this route"
		if r.label != r.name {
			detail = r.label + " — " + detail
		}
		out = append(out, facts.Evidence{Fact: r.name, File: r.file, Detail: detail})
	}
	return out
}
