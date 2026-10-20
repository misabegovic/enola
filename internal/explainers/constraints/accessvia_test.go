package constraints

import (
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func accessMethod(name, file string, rels ...facts.Relation) facts.Fact {
	return facts.Fact{Kind: facts.KindSymbol, Name: name, File: file, Repo: "app",
		Props: map[string]any{"symbol_kind": "method", "language": "ruby"}, Relations: rels}
}

func accessStore(via string) *facts.Store {
	store := facts.NewStore()
	store.Add(
		componentIntent("billing", "app/billing/**"),
		facts.Fact{Kind: facts.KindIntent, Name: "component: accounts", File: "wiki/p.md",
			Props: map[string]any{"intent_kind": "component", "component": "accounts", "kind": "storage", "where": "table=accounts", "source": "wiki/p.md"}},
		lawIntent("billing-never-writes-accounts", map[string]any{"forbid": "billing", "to": "accounts", "via": via}),
		storage("Account", "app/models/account.rb", "accounts"),
		facts.Fact{Kind: facts.KindSymbol, Name: "Account", File: "app/models/account.rb", Repo: "app", Props: map[string]any{"symbol_kind": "class", "language": "ruby"}},
		accessMethod("Billing::Charge#run", "app/billing/charge.rb",
			facts.Relation{Kind: facts.RelCalls, Target: "Account"},
			facts.Relation{Kind: facts.RelWrites, Target: "Account"}),
		accessMethod("Billing::Statement#print", "app/billing/statement.rb",
			facts.Relation{Kind: facts.RelCalls, Target: "Account"},
			facts.Relation{Kind: facts.RelReads, Target: "Account"}),
	)
	return store
}

// A forbidden writes arrow into a table breaches on the method that writes it
// and not on the one that only reads it. Compiled over calls, as before this
// kind existed, the same arrow breaches on both.
func TestForbidViaWrites_NamesTheWriterNotTheReader(t *testing.T) {
	titles := titlesOf(t, accessStore("writes"), "billing-never-writes-accounts violated")
	if len(titles) != 1 || !strings.Contains(titles[0], "Billing::Charge#run -> Account via writes") {
		t.Fatalf("want the writer alone, got %v", titles)
	}
	calls := titlesOf(t, accessStore("calls"), "billing-never-writes-accounts violated")
	if len(calls) != 2 {
		t.Fatalf("over calls the reader breaches beside the writer, got %v", calls)
	}
}

// A writes rule over repositories whose extraction measured no access edge holds
// over nothing, so it refuses by name instead of reading as kept.
func TestAccessVia_RefusesWhereNoAccessIsMeasured(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("billing", "src/billing/**"),
		facts.Fact{Kind: facts.KindIntent, Name: "component: accounts", File: "wiki/p.md",
			Props: map[string]any{"intent_kind": "component", "component": "accounts", "kind": "storage", "match": "db/**", "source": "wiki/p.md"}},
		lawIntent("billing-never-writes-accounts", map[string]any{"forbid": "billing", "to": "accounts", "via": "writes"}),
		facts.Fact{Kind: facts.KindSymbol, Name: "src/billing.charge", File: "src/billing/charge.ts", Repo: "web",
			Props: map[string]any{"symbol_kind": "function", "language": "typescript"}, Relations: []facts.Relation{{Kind: facts.RelCalls, Target: "src/api.post"}}},
	)
	got, err := New().Explain(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	var hits []facts.Insight
	for _, i := range got {
		if strings.Contains(i.Title, "billing-never-writes-accounts") {
			hits = append(hits, i)
		}
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Title, "cannot be evaluated: no access edges") {
		t.Fatalf("want the named refusal alone, got %+v", hits)
	}
	if !strings.Contains(hits[0].Evidence[0].Detail, "cause "+CauseNoAccessEdges) || !strings.Contains(hits[0].Description, "web") {
		t.Fatalf("the refusal names its cause and the repositories it looked in, got %+v", hits[0])
	}
}
