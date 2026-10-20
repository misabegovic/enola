package constraints

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

// unionComponent is a component compiled by one repository of a union: the
// fact carries the declaring repository's label and its repo-prefixed file,
// as an appended snapshot leaves it.
func unionComponent(repo, name, match string) facts.Fact {
	f := componentIntent(name, match)
	f.Repo = repo
	f.File = repo + "/enola/constraints/layers.yaml"
	f.Props["source"] = "enola/constraints/layers.yaml"
	return f
}

func unionRule(repo, id, forbid, to string) facts.Fact {
	f := ruleIntent(id, forbid, to, "calls", "the domain does not know the page that shows it")
	f.Repo = repo
	f.File = repo + "/enola/constraints/layers.yaml"
	f.Props["source"] = "enola/constraints/layers.yaml"
	return f
}

func unionSymbol(repo, name, file, calls string) facts.Fact {
	f := facts.Fact{Kind: facts.KindSymbol, Name: name, File: repo + "/" + file, Repo: repo}
	if calls != "" {
		f.Relations = []facts.Relation{{Kind: facts.RelCalls, Target: calls}}
	}
	return f
}

// unionStore is two repositories that each call their own layer core, with
// different selectors. b's lib/core/ subtree matches a's selector once the
// repository prefix is trimmed, so a rule that read a's core would find a
// breach there instead of in b's own domain.
func unionStore(extra ...facts.Fact) *facts.Store {
	store := facts.NewStore()
	store.Add(
		unionComponent("a", "core", "lib/core/**"),
		unionComponent("a", "ledger", "lib/ledger/**"),
		unionComponent("b", "core", "src/domain/**"),
		unionComponent("b", "web", "src/web/**"),
		unionSymbol("a", "a.core.Rate", "lib/core/rate.go", ""),
		unionSymbol("a", "a.ledger.Post", "lib/ledger/post.go", ""),
		unionSymbol("b", "b.domain.Order", "src/domain/order.go", "b.web.Page"),
		unionSymbol("b", "b.core.Util", "lib/core/util.go", "b.web.Page"),
		unionSymbol("b", "b.web.Page", "src/web/page.go", "a.ledger.Post"),
	)
	store.Add(extra...)
	return store
}

func unionTitles(t *testing.T, store *facts.Store, needle string) []string {
	t.Helper()
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, in := range insights {
		if strings.Contains(in.Title, needle) {
			titles = append(titles, in.Title)
		}
	}
	return titles
}

func TestUnion_ARuleReadsItsOwnRepositorysComponentFirst(t *testing.T) {
	store := unionStore(unionRule("b", "b-domain-avoids-web", "core", "web"))
	got := unionTitles(t, store, "b-domain-avoids-web")
	want := []string{"Constraint b-domain-avoids-web violated: b.domain.Order -> b.web.Page via calls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %q, want %q: b's rule names core, b declares core, and b's core is src/domain/**, not a's lib/core/**", got, want)
	}
}

func TestUnion_ANameTwoOtherRepositoriesDeclareIsRefusedByName(t *testing.T) {
	store := unionStore(unionRule("c", "c-core-avoids-web", "core", "web"))
	if got := unionTitles(t, store, "c-core-avoids-web violated"); len(got) != 0 {
		t.Fatalf("breaches = %q, want none: a and b both declare core and c does not, so no reading of core is c's", got)
	}
	got := unionTitles(t, store, "c-core-avoids-web")
	want := []string{"Constraint rule c-core-avoids-web cannot be evaluated: component core is declared by a and b, not by c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %q, want %q", got, want)
	}
}

func TestUnion_ANameOneOtherRepositoryDeclaresStillResolvesThere(t *testing.T) {
	store := unionStore(unionRule("b", "b-web-avoids-ledger", "web", "ledger"))
	got := unionTitles(t, store, "b-web-avoids-ledger")
	want := []string{"Constraint b-web-avoids-ledger violated: b.web.Page -> a.ledger.Post via calls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %q, want %q: only a declares ledger, so b's rule reads a's, as every cross-repository reference did", got, want)
	}
}

// The shape of a law like one-owner-per-table: a component with no service,
// declared by a repository that holds none of the facts, ranges over every
// repository in the union. Scoping by name must not narrow what it selects.
func TestUnion_AComponentWithNoServiceStillRangesOverOtherRepositories(t *testing.T) {
	tables := facts.Fact{Kind: facts.KindIntent, Name: "component: tables", File: "docs/enola/constraints/laws.yaml", Repo: "docs",
		Props: map[string]any{"intent_kind": "component", "component": "tables", "kind": "storage", "match": "app/models/**", "source": "enola/constraints/laws.yaml"}}
	law := lawIntent("one-owner-per-table", map[string]any{"unique_across": "tables", "by": "table"})
	law.Repo = "docs"
	store := facts.NewStore()
	store.Add(
		tables,
		law,
		facts.Fact{Kind: facts.KindStorage, Name: "Candidate", File: "ats/app/models/candidate.rb", Repo: "ats", Props: map[string]any{"table": "candidates"}},
		facts.Fact{Kind: facts.KindStorage, Name: "Person", File: "crm/app/models/person.rb", Repo: "crm", Props: map[string]any{"table": "candidates"}},
		facts.Fact{Kind: facts.KindStorage, Name: "Deal", File: "crm/app/models/deal.rb", Repo: "crm", Props: map[string]any{"table": "deals"}},
	)
	titles := titlesOf(t, store, "one-owner-per-table")
	if len(titles) != 1 || !strings.Contains(titles[0], "table candidates is owned by ats and crm") {
		t.Fatalf("want the shared table named with both owners, got %v", titles)
	}
}

// Every role that can hold a component name is rewritten by renameComponents,
// read off the vocabulary's own tables so a form or role added later without
// a rename fails here rather than reading a union by bare name in one role.
func TestUnion_RenameReachesEveryRoleThatNamesAComponent(t *testing.T) {
	props := map[string]any{"intent_kind": "rule", "rule": "every-role", "owns": "core=methods", "among": "core"}
	for _, form := range intent.RuleForms {
		props[form.Key] = "core"
	}
	for _, role := range intent.CounterpartRoles {
		props[role.Key] = "core"
	}
	r := decodeRule(facts.Fact{Kind: facts.KindIntent, Name: "rule: every-role", Props: props})
	r.renameComponents(func(name string) string { return name + "@a" })

	v := reflect.ValueOf(r)
	for i := 0; i < v.NumField(); i++ {
		field, name := v.Field(i), v.Type().Field(i).Name
		switch field.Kind() {
		case reflect.String:
			if field.String() == "core" {
				t.Errorf("rule.%s still reads the bare name after the rename", name)
			}
		case reflect.Slice:
			if field.Type().Elem().Kind() != reflect.String {
				continue
			}
			for j := 0; j < field.Len(); j++ {
				if field.Index(j).String() == "core" {
					t.Errorf("rule.%s[%d] still reads the bare name after the rename", name, j)
				}
			}
		case reflect.Map:
			for _, key := range field.MapKeys() {
				if key.String() == "core" {
					t.Errorf("rule.%s keeps the bare name as a key after the rename", name)
				}
			}
		}
	}
}
