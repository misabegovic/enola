package constraints

import (
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// A part reading counts what a forbid verdict over the same pair counts, so
// a mined "no edge crosses" and a declared rule's clean verdict agree.
func TestReadParts_CountsWhatTheForbidVerdictCounts(t *testing.T) {
	store := radiusStore()
	store.Add(rubyMethod("Order#tax", "app/models/order.rb", "OrdersController#render_total", "Order#total"))
	reading := ReadParts(store, []string{facts.RelCalls}, 5)
	flow, ok := reading.Flow("models", facts.RelCalls)
	if !ok {
		t.Fatalf("no models flow: %+v", reading.Flows)
	}
	insights, err := New().Explain(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	breaches := 0
	for _, in := range insights {
		if strings.HasPrefix(in.Title, "Constraint models-stay-off-controllers violated") {
			breaches++
		}
	}
	if breaches == 0 || flow.Landed("controllers") != breaches {
		t.Fatalf("reading counts %d crossings into controllers, the verdict %d", flow.Landed("controllers"), breaches)
	}
	if flow.Inside != 1 || flow.Resolved != flow.Inside+flow.Crossed()+flow.Elsewhere {
		t.Fatalf("flow = %+v", flow)
	}
	if len(flow.CrossingsInto("controllers")) != breaches {
		t.Fatalf("crossings = %+v", flow.CrossingsInto("controllers"))
	}
}

func TestReadParts_NamesThePartsItCannotRead(t *testing.T) {
	store := radiusStore()
	store.Add(componentIntent("empty", "app/nothing/**"))
	reading := ReadParts(store, []string{facts.RelCalls}, 0)
	found := false
	for _, u := range reading.Unread {
		if u.Name == "empty" && strings.Contains(u.Reason, "admits no measured fact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unread = %+v", reading.Unread)
	}
	for _, p := range reading.Parts {
		if p.Name == "empty" {
			t.Fatal("a part with no member was read as one")
		}
	}
}

// A part's fan-in and fan-out are counted by kind, and only as the verdicts
// read them: a fact in another part's file, or of a kind no part holds as a
// member, that shares a member's name adds nothing to the part's fan-out,
// where the name walk counted its edges as the part's.
func TestExplainPart_CountsEdgesByKindAsTheVerdictsRead(t *testing.T) {
	store := radiusStore()
	store.Add(
		facts.Fact{Kind: facts.KindTestRef, Name: "Order#total", File: "spec/order_spec.rb", Repo: "app",
			Relations: []facts.Relation{{Kind: facts.RelCalls, Target: "SyncJob#perform"}}},
		rubyMethod("Order#total", "app/jobs/order_total_job.rb", "SyncJob#perform"),
		facts.Fact{Kind: facts.KindDependency, Name: "app/models/order.rb -> app/controllers/orders_controller.rb", File: "app/models/order.rb", Repo: "app",
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "app/controllers/orders_controller.rb"}}},
	)
	p, ok := ExplainPart(store, "models")
	if !ok {
		t.Fatal("models is declared")
	}
	out := map[string]map[string]int{}
	for _, e := range p.FanOut {
		out[e.Component] = e.Kinds
	}
	if out["controllers"]["calls"] != 1 || out["controllers"]["imports"] != 1 {
		t.Fatalf("fan-out = %+v", p.FanOut)
	}
	if out["jobs"] != nil {
		t.Fatalf("a test reference named like a member was counted as the part's call: %+v", p.FanOut)
	}
	made := map[string]PartMade{}
	for _, m := range p.Made {
		made[m.Kind] = m
	}
	if made["calls"].Made != 1 || made["imports"].Made != 1 {
		t.Fatalf("made = %+v", p.Made)
	}
}
