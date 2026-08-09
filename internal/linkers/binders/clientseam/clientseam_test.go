package clientseam

import (
	"context"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func symbol(repo, name string, calls ...string) facts.Fact {
	f := facts.Fact{Kind: facts.KindSymbol, Name: name, Repo: repo, File: name + ".go"}
	for _, c := range calls {
		f.Relations = append(f.Relations, facts.Relation{Kind: facts.RelCalls, Target: c})
	}
	return f
}

func withCandidates(f facts.Fact, candidates ...string) facts.Fact {
	f.Props = map[string]any{"client_path_calls": candidates}
	return f
}

func clientRoutes(store *facts.Store) map[string]string {
	out := map[string]string{}
	for _, f := range store.All() {
		if f.Kind == facts.KindRoute && f.PropString(facts.PropRole) == facts.RoleClient {
			out[f.PropString("method")+" "+f.Name] = f.PropString("seam")
		}
	}
	return out
}

// The measured case. A command calls a project-local helper with a literal path;
// the helper does not touch net/http itself, it calls another one that does. A
// one-hop rule finds the inner helper nobody calls and misses the outer one
// everybody does.
func TestBind_ResolvesASeamTwoHopsFromNetHTTP(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("cli", "internal/api.doRequest", "net/http.NewRequestWithContext"),
		symbol("cli", "internal/api.Request", "internal/api.doRequest"),
		withCandidates(symbol("cli", "internal/cli.tasksCmd", "internal/api.Request"),
			"internal/api.Request\x00/me/tasks\x00GET",
			"internal/api.Request\x00/me/tasks\x00POST"),
	)

	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	routes := clientRoutes(store)
	for _, want := range []string{"GET /me/tasks", "POST /me/tasks"} {
		if routes[want] != "internal/api.Request" {
			t.Errorf("want %q via internal/api.Request, got %v", want, routes)
		}
	}
}

// A helper that never reaches net/http is not a seam, however path-shaped its
// argument. `log.Printf("/me/tasks")` and a router registration both look like
// this from the call site, which is why the call site does not get to decide.
func TestBind_CandidateWithNoSeamIsNotARoute(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("cli", "internal/fsutil.Load"),
		withCandidates(symbol("cli", "internal/cli.loadCmd", "internal/fsutil.Load"),
			"internal/fsutil.Load\x00/etc/config\x00GET"),
	)

	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if routes := clientRoutes(store); len(routes) != 0 {
		t.Errorf("nothing here reaches net/http, so nothing is a client route; got %v", routes)
	}
}

// The candidate list is scaffolding: it names calls that may be nothing and is
// keyed on an internal separator. It leaves, whether or not it resolved, so a
// repo with no seam is left exactly as clean as one with a resolved seam.
func TestBind_CandidatePropIsRemovedEitherWay(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("cli", "internal/api.doRequest", "net/http.NewRequestWithContext"),
		withCandidates(symbol("cli", "internal/cli.a", "internal/api.doRequest"),
			"internal/api.doRequest\x00/me/tasks\x00GET"),
		withCandidates(symbol("cli", "internal/cli.b", "internal/fsutil.Load"),
			"internal/fsutil.Load\x00/etc/config\x00GET"),
	)

	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, f := range store.All() {
		if f.Kind != facts.KindSymbol {
			continue
		}
		if _, has := f.Props["client_path_calls"]; has {
			t.Errorf("%s kept the scaffolding prop; props=%+v", f.Name, f.Props)
		}
	}
}

// Two repos with the same package layout must not share a seam. The reachability
// walk is keyed by repo for that reason, and a cluster snapshot routinely holds
// several Go services whose internal package paths are identical.
func TestBind_SeamsDoNotCrossRepos(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("alpha", "internal/api.doRequest", "net/http.NewRequestWithContext"),
		withCandidates(symbol("zulu", "internal/cli.cmd", "internal/api.doRequest"),
			"internal/api.doRequest\x00/me/tasks\x00GET"),
	)

	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if routes := clientRoutes(store); len(routes) != 0 {
		t.Errorf("zulu has no seam of its own; got %v", routes)
	}
}
