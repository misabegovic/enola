package constraints

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// overlapStore declares two components whose selectors both admit
// app/shared/money, with an edge from it into the second component's own
// member and one back, so the shared file sits on both sides of every rule
// below and has a path that returns to it.
func overlapStore(rules ...facts.Fact) *facts.Store {
	store := facts.NewStore()
	store.Add(
		componentIntent("parts", "app/shared/** app/parts/**"),
		componentIntent("core", "app/shared/** app/core/**"),
		facts.Fact{Kind: facts.KindModule, Name: "app/shared/money", File: "app/shared/money",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/core/ledger"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/core/ledger", File: "app/core/ledger",
			Relations: []facts.Relation{{Kind: facts.RelDependsOn, Target: "app/shared/money"}}},
		facts.Fact{Kind: facts.KindModule, Name: "app/parts/invoice", File: "app/parts/invoice"},
	)
	store.Add(rules...)
	return store
}

func overlapVerdicts(t *testing.T, store *facts.Store, id string) []string {
	t.Helper()
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, in := range insights {
		if strings.HasPrefix(in.Title, "Constraint "+id+" violated: ") {
			titles = append(titles, in.Title)
		}
	}
	return titles
}

func TestOverlap_AFactIsAMemberOfEveryComponentThatSelectsIt(t *testing.T) {
	index := NewMembershipIndex(overlapStore().All())
	for _, component := range []string{"parts", "core"} {
		if !index.Selects(component, "app/shared/money") {
			t.Errorf("%s does not select app/shared/money, which its own selector admits", component)
		}
	}
	if index.Selects("parts", "app/core/ledger") || index.Selects("core", "app/parts/invoice") {
		t.Error("an overlap must not widen either selector past its own patterns")
	}
}

func TestOverlap_ForbidBreachesOnAnEdgeFromAMemberOfBoth(t *testing.T) {
	store := overlapStore(ruleIntent("parts-avoid-core", "parts", "core", "depends_on", "parts are composed, never composing"))
	got := overlapVerdicts(t, store, "parts-avoid-core")
	want := []string{"Constraint parts-avoid-core violated: app/shared/money -> app/core/ledger via depends_on"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %q, want %q: the edge leaves a member of parts and lands on a member of core, and being in core as well subtracts nothing", got, want)
	}
}

func TestOverlap_ForbidReachDoesNotCountAMemberOfBothReachingItself(t *testing.T) {
	store := overlapStore(reachRuleIntent("parts-never-reach-core", "parts", "core", "depends_on", "parts are composed, never composing"))
	got := overlapVerdicts(t, store, "parts-never-reach-core")
	want := []string{"Constraint parts-never-reach-core violated: app/shared/money reaches app/core/ledger"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %q, want %q: the path back to app/shared/money through the ledger is the shared file reaching itself, which is an overlap and not a measured reach", got, want)
	}
}
