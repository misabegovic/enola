package constraints

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

// declaredWorld compiles a declaration the way the engine does and adds the
// measured facts beside it.
func declaredWorld(t *testing.T, declaration string, measured ...facts.Fact) *facts.Store {
	t.Helper()
	var d intent.Declaration
	if err := yaml.Unmarshal([]byte(declaration), &d); err != nil {
		t.Fatal(err)
	}
	d.Normalize()
	d.Source = intent.RepoFileName
	if problems := d.Problems(); len(problems) > 0 {
		t.Fatalf("declaration invalid: %s", strings.Join(problems, "; "))
	}
	store := facts.NewStore()
	store.Add(measured...)
	store.Add(intent.CompileFacts(&d)...)
	return store
}

const unverdictedDeclaration = `
components:
  - name: api
    match: ["api/**"]
  - name: store
    match: ["store/**"]
  - name: ghost
    match: ["ghost/**"]
  - name: remote
    service: elsewhere
  - name: odd
    match: ["api/**"]
    where: {frobnicated: "yes"}
  - name: mixins
    match: ["concerns/**"]
rules:
  - id: api-never-calls-store
    forbid: api
    to: store
    via: calls
    because: the verdicted one.
  - id: ghost-never-calls-store
    forbid: ghost
    to: store
    via: calls
    because: a subject with no members.
  - id: api-never-calls-ghost
    forbid: api
    to: ghost
    via: calls
    because: a far end with no members.
  - id: remote-never-calls-store
    forbid: remote
    to: store
    via: calls
    because: a service this snapshot does not hold.
  - id: no-ghosts
    forbid_fact: ghost
    because: an empty part is what this form asks for.
  - id: few-ghosts
    cap: ghost
    max_members: 2
    because: none is under the cap.
  - id: few-odd
    cap: odd
    max_members: 2
    because: a selector over a property nothing measures.
  - id: mixins-stay-out
    independent: mixins
    because: resolved ancestry is what this form reads.
  - id: api-calls-carry-a-tag
    require: api
    when_edge_to: ["Missing*"]
    via: calls
    must_prop_contain: {prop: tags, value: audited}
    because: an antecedent that selects nobody.
`

func unverdictedWorld(t *testing.T) *facts.Store {
	return declaredWorld(t, unverdictedDeclaration,
		facts.Fact{Kind: facts.KindSymbol, Name: "api.Serve", File: "api/serve.go", Repo: "shop",
			Props:     map[string]any{"symbol_kind": "function"},
			Relations: []facts.Relation{{Kind: facts.RelCalls, Target: "store.Put"}}},
		facts.Fact{Kind: facts.KindSymbol, Name: "store.Put", File: "store/put.go", Repo: "shop",
			Props: map[string]any{"symbol_kind": "function"}},
		facts.Fact{Kind: facts.KindModule, Name: "concerns", File: "concerns/audit.rb", Repo: "shop"},
	)
}

// Each rule the snapshot reached no verdict on is named with the reason the
// explainer had, and the rules it verdicted, including the two whose form an
// empty part satisfies, are not.
func TestUnverdictedRulesNameTheirReason(t *testing.T) {
	store := unverdictedWorld(t)
	findings := explain(t, store)
	got := UnverdictedRules(store, findings)

	want := map[string]string{
		"ghost-never-calls-store":  UnverdictedMatchesNothing,
		"api-never-calls-ghost":    UnverdictedMatchesNothing,
		"remote-never-calls-store": UnverdictedServiceAbsent,
		"few-odd":                  UnverdictedSelectorUnevaluable,
		"mixins-stay-out":          UnverdictedFormUnevaluable,
		"api-calls-carry-a-tag":    UnverdictedSkipped,
	}
	for id, reason := range want {
		u, ok := got[id]
		if !ok {
			t.Errorf("%s: reported verdicted, want %s", id, reason)
			continue
		}
		if u.Reason != reason {
			t.Errorf("%s: reason %s (%s), want %s", id, u.Reason, u.Detail, reason)
		}
		if u.Detail == "" {
			t.Errorf("%s: no detail", id)
		}
	}
	for _, id := range []string{"api-never-calls-store", "no-ghosts", "few-ghosts"} {
		if u, ok := got[id]; ok {
			t.Errorf("%s: reported unverdicted (%s: %s), want verdicted", id, u.Reason, u.Detail)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unverdicted = %v, want exactly %d rules", got, len(want))
	}
	for _, reason := range got {
		known := false
		for _, r := range UnverdictedReasons {
			known = known || r == reason.Reason
		}
		if !known {
			t.Errorf("reason %q is outside UnverdictedReasons", reason.Reason)
		}
	}
	if d := got["remote-never-calls-store"].Detail; !strings.Contains(d, "elsewhere") {
		t.Errorf("service_absent detail %q does not name the service", d)
	}
	if d := got["ghost-never-calls-store"].Detail; !strings.Contains(d, "ghost") {
		t.Errorf("matches_nothing detail %q does not name the part", d)
	}
}

// A rule's findings open with the sentence it was written from, and the
// sentence is bookkeeping: adding or changing it leaves the declaration's
// identity where it was, so no standing breach reads as newly declared.
func TestSaidRidesTheFindingsAndMovesNoDeclaration(t *testing.T) {
	with := strings.Replace(unverdictedDeclaration, "    because: the verdicted one.",
		"    said: \"api never calls store\"\n    because: the verdicted one.", 1)
	store := declaredWorld(t, with,
		facts.Fact{Kind: facts.KindSymbol, Name: "api.Serve", File: "api/serve.go", Repo: "shop",
			Relations: []facts.Relation{{Kind: facts.RelCalls, Target: "store.Put"}}},
		facts.Fact{Kind: facts.KindSymbol, Name: "store.Put", File: "store/put.go", Repo: "shop"},
	)
	var breach *facts.Insight
	findings := explain(t, store)
	for i := range findings {
		if strings.HasPrefix(findings[i].Title, "Constraint api-never-calls-store violated: ") {
			breach = &findings[i]
		}
	}
	if breach == nil {
		t.Fatalf("no breach of api-never-calls-store in %v", findings)
	}
	if !strings.HasPrefix(breach.Description, `Said: "api never calls store". `) {
		t.Errorf("description does not open with the sentence: %q", breach.Description)
	}
	if !strings.Contains(breach.Description, "Because: the verdicted one.") {
		t.Errorf("the closing Because moved: %q", breach.Description)
	}

	without := unverdictedWorld(t)
	a, _ := NewMembershipIndex(store.FactsRef()).Declaration("api-never-calls-store")
	b, _ := NewMembershipIndex(without.FactsRef()).Declaration("api-never-calls-store")
	if a != b {
		t.Errorf("said moved the declaration identity:\n%s\n---\n%s", a, b)
	}
}
