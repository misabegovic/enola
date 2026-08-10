package constraints

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func componentIntent(name, match string) facts.Fact {
	return facts.Fact{Kind: facts.KindIntent, Name: "component: " + name, File: "wiki/p.md",
		Props: map[string]any{"intent_kind": "component", "component": name, "match": match, "source": "wiki/p.md"}}
}

func ruleIntent(id, forbid, to, via, because string) facts.Fact {
	return facts.Fact{Kind: facts.KindIntent, Name: "rule: " + id, File: "wiki/p.md",
		Props: map[string]any{"intent_kind": "rule", "rule": id, "forbid": forbid, "to": to,
			"via": via, "because": because, "source": "wiki/p.md"}}
}

func TestExplain_ForbiddenEdgeIsAProofClassViolation(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("domain", "app/domain/**"),
		componentIntent("adapters", "app/adapters/**"),
		ruleIntent("domain-stays-pure", "domain", "adapters", "depends_on", "the domain must not know its delivery mechanisms"),
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/billing", File: "app/domain/billing",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/adapters/http"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/adapters/http", File: "app/adapters/http"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 1 {
		t.Fatalf("insights = %d, want exactly 1: %+v", len(insights), insights)
	}
	got := insights[0]
	want := "Constraint domain-stays-pure violated: app/domain/billing -> app/adapters/http via depends_on"
	if got.Title != want {
		t.Errorf("title = %q, want %q", got.Title, want)
	}
	if got.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0: a declared rule's breach is decided, not estimated", got.Confidence)
	}
	if !strings.Contains(got.Description, "Because: the domain must not know its delivery mechanisms") {
		t.Errorf("description must surface the rule's rationale, got: %q", got.Description)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].File != "app/domain/billing" ||
		got.Evidence[0].Symbol != "app/domain/billing" || got.Evidence[0].Fact != "app/adapters/http" {
		t.Errorf("evidence = %+v, want the source file/name and target name", got.Evidence)
	}
}

func TestExplain_NonViolatingEdgeIsSilence(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("domain", "app/domain/**"),
		componentIntent("adapters", "app/adapters/**"),
		ruleIntent("domain-stays-pure", "domain", "adapters", "depends_on", "the domain must not know its delivery mechanisms"),
		// The allowed direction: an adapter reaching into the domain.
		facts.Fact{Kind: facts.KindModule, Name: "app/adapters/http", File: "app/adapters/http",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/domain/billing"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/billing", File: "app/domain/billing"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 0 {
		t.Fatalf("insights = %+v, want none: an agreeing verdict is silence", insights)
	}
}

func TestExplain_EmptyComponentGetsTheDeadSelectorAdvisory(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("ghost", "app/ghost/**"),
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/billing", File: "app/domain/billing"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 1 {
		t.Fatalf("insights = %d, want exactly 1: %+v", len(insights), insights)
	}
	if got := insights[0].Title; got != "Constraint component ghost matches nothing" {
		t.Errorf("title = %q", got)
	}
	if got := insights[0].Confidence; got != emptyComponentConfidence {
		t.Errorf("confidence = %v, want %v: a dead selector is an advisory, never a breach", got, emptyComponentConfidence)
	}
}

func TestExplain_TargetResolutionFailsClosed(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("domain", "app/domain/**"),
		componentIntent("adapters", "app/adapters/**"),
		ruleIntent("domain-stays-pure", "domain", "adapters", "depends_on", "the domain must not know its delivery mechanisms"),
		// The target string does not exactly name the adapter fact, so no
		// membership can be proven — and an unprovable match is no violation.
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/billing", File: "app/domain/billing",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "http"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/adapters/http", File: "app/adapters/http"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 0 {
		t.Fatalf("insights = %+v, want none: an unresolvable target must never be guessed into a breach", insights)
	}
}

func TestExplain_OutputIsDeterministic(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("domain", "app/domain/**"),
		componentIntent("adapters", "app/adapters/**"),
		componentIntent("ghost", "app/ghost/**"),
		ruleIntent("domain-stays-pure", "domain", "adapters", "depends_on", "the domain must not know its delivery mechanisms"),
		ruleIntent("no-domain-calls", "domain", "adapters", "calls", "runtime coupling counts too"),
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/billing", File: "app/domain/billing",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/adapters/http"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/domain/pricing", File: "app/domain/pricing",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/adapters/http"}}},
		facts.Fact{Kind: facts.KindSymbol, Name: "app/domain/billing.Invoice.Send", File: "app/domain/billing/invoice.go",
			Relations: []facts.Relation{{Kind: facts.RelCalls, Target: "app/adapters/http.Client.Post"}}},
		facts.Fact{Kind: facts.KindSymbol, Name: "app/adapters/http.Client.Post", File: "app/adapters/http/client.go"},
		facts.Fact{Kind: facts.KindModule, Name: "app/adapters/http", File: "app/adapters/http"},
	)
	first, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 {
		t.Fatalf("insights = %d, want 3 violations + 1 advisory: %+v", len(first), first)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two runs over one store diverged:\n%+v\n%+v", first, second)
	}
	if !sortedByTitle(first) {
		t.Errorf("insights are not title-sorted: %+v", first)
	}
}

func sortedByTitle(insights []facts.Insight) bool {
	for i := 1; i < len(insights); i++ {
		if insights[i-1].Title > insights[i].Title {
			return false
		}
	}
	return true
}
