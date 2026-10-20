package facts

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A relation's target_id is the name lookup's answer, and for a Ruby call the
// lookup can answer with a directory, a table or a route: the Ruby extractor
// records a receiverless call by its bare method name, so `config` reaches the
// config/ module and `tags` the tags table. These pin which answers a Ruby call
// keeps, that nothing else moves, and that every other language keeps what it
// had, since the TypeScript and handlebars passes point calls at routes on
// purpose.

// writtenTargetIDs writes the store and returns, for each relation, the kind
// of the fact its target_id names, or "" when it carries none. A relation is
// keyed "source name|relation kind|target".
func writtenTargetIDs(t *testing.T, s *Store) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.WriteJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	type line struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Relations []struct {
			Kind     string `json:"kind"`
			Target   string `json:"target"`
			TargetID string `json:"target_id"`
		} `json:"relations"`
	}
	var lines []line
	kindOf := map[string]string{}
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
		kindOf[l.ID] = l.Kind
	}
	got := map[string]string{}
	for _, l := range lines {
		for _, r := range l.Relations {
			key := l.Name + "|" + r.Kind + "|" + r.Target
			if r.TargetID == "" {
				got[key] = ""
				continue
			}
			kind, ok := kindOf[r.TargetID]
			if !ok {
				t.Fatalf("%s: target_id %s names no fact in the snapshot", key, r.TargetID)
			}
			got[key] = kind
		}
	}
	return got
}

func rubyFact(kind, name, file string, rels ...Relation) Fact {
	return Fact{Kind: kind, Name: name, File: file, Repo: "r", Props: map[string]any{"language": "ruby"}, Relations: rels}
}

func TestCallTargetID_RubyCallOntoANonSymbolGetsNoID(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindModule, Name: "config", File: "config", Repo: "r"},
		Fact{Kind: KindStorage, Name: "tags", File: "db/schema.rb", Repo: "r"},
		Fact{Kind: KindRoute, Name: "/", File: "config/routes.rb", Repo: "r"},
		rubyFact(KindSymbol, "App#boot", "app/app.rb",
			Relation{Kind: RelCalls, Target: "config"},
			Relation{Kind: RelCalls, Target: "tags"},
			Relation{Kind: RelCalls, Target: "/"},
			Relation{Kind: RelInstantiates, Target: "config"},
		),
		rubyFact(KindFileRef, "app/views/a.html.erb", "app/views/a.html.erb",
			Relation{Kind: RelCalls, Target: "config"},
		),
		rubyFact(KindTestRef, "test/app_test.rb", "test/app_test.rb",
			Relation{Kind: RelCalls, Target: "tags"},
		),
	)
	got := writtenTargetIDs(t, s)
	for _, key := range []string{
		"App#boot|calls|config",
		"App#boot|calls|tags",
		"App#boot|calls|/",
		"App#boot|instantiates|config",
		"app/views/a.html.erb|calls|config",
		"test/app_test.rb|calls|tags",
	} {
		if kind := got[key]; kind != "" {
			t.Errorf("%s carries an id onto a %s; a Ruby call reaches only a symbol or an association", key, kind)
		}
	}
}

func TestCallTargetID_RubyCallOntoASymbolOrAnAssociationKeepsItsID(t *testing.T) {
	s := NewStore()
	s.Add(
		rubyFact(KindSymbol, "Attachment", "app/models/attachment.rb"),
		rubyFact(KindSymbol, "Attachment#deletable?", "app/models/attachment.rb",
			Relation{Kind: RelCalls, Target: "Attachment#author"},
			Relation{Kind: RelCalls, Target: "Attachment"},
			Relation{Kind: RelInstantiates, Target: "Attachment"},
		),
		Fact{Kind: KindAssociation, Name: "Attachment#author", File: "app/models/attachment.rb", Repo: "r"},
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"Attachment#deletable?|calls|Attachment#author": KindAssociation,
		"Attachment#deletable?|calls|Attachment":        KindSymbol,
		"Attachment#deletable?|instantiates|Attachment": KindSymbol,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want the %s it named before", key, got[key], want)
		}
	}
}

// The rule drops an id the lookup gave and never adds or moves one. A model's
// class and its table share a name, so a reference to the class has two
// candidates and no id today; giving it the class's id is binding, which is
// not this rule's to do. A module in the calling repository answered first
// today, and dropping it must not hand the name to another repository.
func TestCallTargetID_NoIDIsAddedOrMoved(t *testing.T) {
	s := NewStore()
	s.Add(
		rubyFact(KindSymbol, "User", "app/models/user.rb"),
		Fact{Kind: KindStorage, Name: "User", File: "app/models/user.rb", Repo: "r", Props: map[string]any{"language": "ruby"}},
		Fact{Kind: KindModule, Name: "script", File: "script", Repo: "r"},
		Fact{Kind: KindSymbol, Name: "script", File: "lib/script.rb", Repo: "other"},
		Fact{Kind: KindSymbol, Name: "Report", File: "app/report.rb", Repo: "r"},
		Fact{Kind: KindModule, Name: "Report", File: "Report", Repo: "other"},
		rubyFact(KindSymbol, "Admin#index", "app/admin.rb",
			Relation{Kind: RelCalls, Target: "User"},
			Relation{Kind: RelInstantiates, Target: "User"},
			Relation{Kind: RelCalls, Target: "script"},
			Relation{Kind: RelCalls, Target: "Report"},
		),
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"Admin#index|calls|User":        "",
		"Admin#index|instantiates|User": "",
		"Admin#index|calls|script":      "",
		// A symbol in the calling repository beside a module elsewhere was
		// the lookup's answer already, and stays it.
		"Admin#index|calls|Report": KindSymbol,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want %q", key, got[key], want)
		}
	}
}

// A provider's facts carry no language, so the file they were read from says
// they are Ruby: a Prism call fact is a dependency named for its caller and
// callee, read from a .rb file.
func TestCallTargetID_ProviderFactsAreReadAsRubyByTheirFile(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindModule, Name: "config", File: "config", Repo: "r"},
		Fact{Kind: KindAssociation, Name: "Attachment#author", File: "app/models/attachment.rb", Repo: "r"},
		Fact{Kind: KindDependency, Name: "prism-call: Engine -> config", File: "lib/engine.rb", Repo: "r",
			Props:     map[string]any{"provider": "prism", "resolution_level": "name-only"},
			Relations: []Relation{{Kind: RelCalls, Target: "config"}}},
		Fact{Kind: KindDependency, Name: "prism-call: Attachment#deletable? -> Attachment#author", File: "app/models/attachment.rb", Repo: "r",
			Props:     map[string]any{"provider": "prism", "resolution_level": "lexical-self"},
			Relations: []Relation{{Kind: RelCalls, Target: "Attachment#author"}}},
		Fact{Kind: KindDependency, Name: "rake-call: default -> config", File: "Rakefile", Repo: "r",
			Props:     map[string]any{"provider": "rake"},
			Relations: []Relation{{Kind: RelCalls, Target: "config"}}},
		Fact{Kind: KindDependency, Name: "task-call: seed -> config", File: "lib/tasks/seed.RAKE", Repo: "r",
			Props:     map[string]any{"provider": "rake"},
			Relations: []Relation{{Kind: RelCalls, Target: "config"}}},
		Fact{Kind: KindDependency, Name: "js-call: boot -> config", File: "app/boot.js", Repo: "r",
			Props:     map[string]any{"provider": "eslint"},
			Relations: []Relation{{Kind: RelCalls, Target: "config"}}},
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"prism-call: Engine -> config|calls|config":                                      "",
		"prism-call: Attachment#deletable? -> Attachment#author|calls|Attachment#author": KindAssociation,
		"rake-call: default -> config|calls|config":                                      "",
		"task-call: seed -> config|calls|config":                                         "",
		"js-call: boot -> config|calls|config":                                           KindModule,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want %q", key, got[key], want)
		}
	}
}

func TestCallTargetID_OtherRelationKindsKeepTheirID(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindModule, Name: "app/models", File: "app/models", Repo: "r"},
		Fact{Kind: KindStorage, Name: "posts", File: "db/schema.rb", Repo: "r"},
		rubyFact(KindSymbol, "Post", "app/models/post.rb",
			Relation{Kind: RelDeclares, Target: "app/models"},
			Relation{Kind: RelImports, Target: "app/models"},
			Relation{Kind: RelDependsOn, Target: "posts"},
			Relation{Kind: RelReads, Target: "posts"},
		),
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"Post|declares|app/models": KindModule,
		"Post|imports|app/models":  KindModule,
		"Post|depends_on|posts":    KindStorage,
		"Post|reads|posts":         KindStorage,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want the %s it named before", key, got[key], want)
		}
	}
}

// The Ember and React Navigation passes record a link to a route as a call
// onto the route's fact, and that id is the right answer: <LinkTo
// @route="catalog">, router.transitionTo('account') and
// navigation.navigate('Profile') reach exactly the route they name.
func TestCallTargetID_OtherLanguagesKeepCallsOntoRoutes(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindRoute, Name: "/catalog", File: "app/router.ts", Repo: "r", Props: map[string]any{"language": "typescript"}},
		Fact{Kind: KindRoute, Name: "Profile", File: "src/navigation/root.js", Repo: "r", Props: map[string]any{"language": "typescript"}},
		Fact{Kind: KindFileRef, Name: "app/templates/index.hbs", File: "app/templates/index.hbs", Repo: "r",
			Props:     map[string]any{"language": "handlebars"},
			Relations: []Relation{{Kind: RelCalls, Target: "/catalog"}}},
		Fact{Kind: KindSymbol, Name: "src/components.Banner", File: "src/components/banner.js", Repo: "r",
			Props:     map[string]any{"language": "typescript"},
			Relations: []Relation{{Kind: RelCalls, Target: "Profile"}}},
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"app/templates/index.hbs|calls|/catalog": KindRoute,
		"src/components.Banner|calls|Profile":    KindRoute,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want the %s it named before", key, got[key], want)
		}
	}
}

func TestCallTargetID_WritesAreByteIdentical(t *testing.T) {
	build := func(reverse bool) *Store {
		ff := []Fact{
			Fact{Kind: KindModule, Name: "config", File: "config", Repo: "r"},
			Fact{Kind: KindAssociation, Name: "A#b", File: "a.rb", Repo: "r"},
			rubyFact(KindSymbol, "A", "a.rb"),
			rubyFact(KindSymbol, "A#c", "a.rb",
				Relation{Kind: RelCalls, Target: "config"},
				Relation{Kind: RelCalls, Target: "A#b"},
				Relation{Kind: RelInstantiates, Target: "A"},
			),
		}
		if reverse {
			for i, j := 0, len(ff)-1; i < j; i, j = i+1, j-1 {
				ff[i], ff[j] = ff[j], ff[i]
			}
		}
		s := NewStore()
		s.Add(ff...)
		return s
	}
	var first, second, reversed bytes.Buffer
	if err := build(false).WriteJSONL(&first); err != nil {
		t.Fatal(err)
	}
	if err := build(false).WriteJSONL(&second); err != nil {
		t.Fatal(err)
	}
	if err := build(true).WriteJSONL(&reversed); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Error("two writes of the same facts differ")
	}
	if first.String() != reversed.String() {
		t.Error("the order facts were added in changed what was written")
	}
	if first.String() != referenceJSONL(t, build(false)) {
		t.Error("the writer and the string-sort reference disagree on which ids a Ruby call keeps")
	}
}

// In a union the name lookup reaches into every repository when the caller's
// own holds nothing of the name, and a call's bare name meets facts that share
// it by coincidence: a Rust `Walk::default()` derived from `Default` meets a
// Ruby job queue named default, and a method named like a repository meets the
// service node standing for that repository. Neither is what the call reaches.
func TestCallTargetID_ACallOntoAnotherRepositorysNonSymbolGetsNoID(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindStorage, Name: "default", File: "jobs/app/jobs/report_job.rb", Repo: "jobs",
			Props: map[string]any{"language": "ruby", "storage_kind": "topic"}},
		Fact{Kind: KindModule, Name: "config", File: "jobs/config", Repo: "jobs"},
		Fact{Kind: KindRoute, Name: "/reports", File: "jobs/config/routes.rb", Repo: "jobs"},
		Fact{Kind: KindSymbol, Name: "crates/pic/src.Walk", File: "atlas/crates/pic/src/lib.rs", Repo: "atlas",
			Props: map[string]any{"language": "rust"}},
		Fact{Kind: KindSymbol, Name: "crates/pic/src.write", File: "atlas/crates/pic/src/lib.rs", Repo: "atlas",
			Props: map[string]any{"language": "rust", "unresolved_calls": "no_method_on_type=1"},
			Relations: []Relation{
				{Kind: RelCalls, Target: "default"},
				{Kind: RelCalls, Target: "config"},
				{Kind: RelCalls, Target: "/reports"},
				{Kind: RelInstantiates, Target: "config"},
				{Kind: RelInstantiates, Target: "crates/pic/src.Walk"},
			}},
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"crates/pic/src.write|calls|default":                    "",
		"crates/pic/src.write|calls|config":                     "",
		"crates/pic/src.write|calls|/reports":                   "",
		"crates/pic/src.write|instantiates|config":              "",
		"crates/pic/src.write|instantiates|crates/pic/src.Walk": KindSymbol,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want %q", key, got[key], want)
		}
	}
}

func TestCallTargetID_ACallOntoAServiceNodeGetsNoID(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindService, Name: "atlas", Repo: "atlas", Props: map[string]any{"synthetic": "crossrepo"}},
		Fact{Kind: KindService, Name: "jobs", Repo: "jobs", Props: map[string]any{"synthetic": "crossrepo"}},
		Fact{Kind: KindSymbol, Name: "crates/pic/src.Kind.atlas", File: "atlas/crates/pic/src/lib.rs", Repo: "atlas",
			Props: map[string]any{"language": "rust", "receiver": "Kind"}},
		Fact{Kind: KindSymbol, Name: "crates/pic/src.placed", File: "atlas/crates/pic/src/lib.rs", Repo: "atlas",
			Props: map[string]any{"language": "rust", "unresolved_calls": "receiver_expression=1"},
			Relations: []Relation{
				{Kind: RelCalls, Target: "atlas"},
				{Kind: RelCalls, Target: "jobs"},
				{Kind: RelInstantiates, Target: "atlas"},
			}},
		Fact{Kind: KindFileRef, Name: "atlas/app/templates/index.hbs", File: "atlas/app/templates/index.hbs", Repo: "atlas",
			Props:     map[string]any{"language": "handlebars"},
			Relations: []Relation{{Kind: RelCalls, Target: "atlas"}}},
	)
	got := writtenTargetIDs(t, s)
	for _, key := range []string{
		"crates/pic/src.placed|calls|atlas",
		"crates/pic/src.placed|calls|jobs",
		"crates/pic/src.placed|instantiates|atlas",
		"atlas/app/templates/index.hbs|calls|atlas",
	} {
		if kind := got[key]; kind != "" {
			t.Errorf("%s carries an id onto a %s; a service node stands for a repository, and no call reaches one", key, kind)
		}
	}
}

// What a union is for must survive: a call onto a symbol another repository
// declares, a service's dependency on another service, an import or a call
// through the alias a consumer's own import wrote, and every non-call relation
// onto another repository's module or table.
func TestCallTargetID_CrossRepositoryEdgesThatLandKeepTheirID(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindSymbol, Name: "billing.internal.Charge", File: "billing/internal/charge.go", Repo: "billing",
			Props: map[string]any{"language": "go"}},
		Fact{Kind: KindAssociation, Name: "Invoice#lines", File: "billing/app/models/invoice.rb", Repo: "billing"},
		Fact{Kind: KindModule, Name: "adapters", File: "billing/adapters", Repo: "billing"},
		Fact{Kind: KindSymbol, Name: "adapters.Client.Charge", File: "billing/adapters/client.go", Repo: "billing",
			Props: map[string]any{"language": "go"}},
		Fact{Kind: KindStorage, Name: "invoices", File: "billing/db/schema.rb", Repo: "billing"},
		Fact{Kind: KindService, Name: "billing", Repo: "billing", Props: map[string]any{"synthetic": "crossrepo"}},
		Fact{Kind: KindService, Name: "frontend", Repo: "frontend", Props: map[string]any{"synthetic": "crossrepo"},
			Relations: []Relation{{Kind: RelDependsOn, Target: "billing"}}},
		Fact{Kind: KindSymbol, Name: "web.Checkout", File: "frontend/web/checkout.go", Repo: "frontend",
			Props: map[string]any{"language": "go"},
			Relations: []Relation{
				{Kind: RelCalls, Target: "billing.internal.Charge"},
				{Kind: RelCalls, Target: "Invoice#lines"},
				{Kind: RelCalls, Target: "github.com/acme/billing/adapters.Client.Charge"},
				{Kind: RelImports, Target: "github.com/acme/billing/adapters"},
				{Kind: RelImports, Target: "adapters"},
				{Kind: RelReads, Target: "invoices"},
			}},
	)
	s.SetTargetAliases(map[string]FactKey{
		"github.com/acme/billing/adapters":               {Repo: "billing", Kind: KindModule, Name: "adapters", File: "billing/adapters"},
		"github.com/acme/billing/adapters.Client.Charge": {Repo: "billing", Kind: KindSymbol, Name: "adapters.Client.Charge", File: "billing/adapters/client.go"},
	})
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"web.Checkout|calls|billing.internal.Charge":                        KindSymbol,
		"web.Checkout|calls|Invoice#lines":                                  KindAssociation,
		"web.Checkout|calls|github.com/acme/billing/adapters.Client.Charge": KindSymbol,
		"web.Checkout|imports|github.com/acme/billing/adapters":             KindModule,
		"web.Checkout|imports|adapters":                                     KindModule,
		"web.Checkout|reads|invoices":                                       KindStorage,
		"frontend|depends_on|billing":                                       KindService,
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want the %s it named before", key, got[key], want)
		}
	}
}

// A call's own repository is where a same-named module or route is its
// language's to decide, as before: an Ember link onto its own route keeps the
// id, and a route of the same name in another repository is never preferred
// over it nor reached when the caller's repository holds none.
func TestCallTargetID_ACallStillLandsInItsOwnRepository(t *testing.T) {
	s := NewStore()
	s.Add(
		Fact{Kind: KindRoute, Name: "catalog", File: "shop/app/router.ts", Repo: "shop", Props: map[string]any{"language": "typescript"}},
		Fact{Kind: KindRoute, Name: "catalog", File: "admin/app/router.ts", Repo: "admin", Props: map[string]any{"language": "typescript"}},
		Fact{Kind: KindRoute, Name: "account", File: "admin/app/router.ts", Repo: "admin", Props: map[string]any{"language": "typescript"}},
		Fact{Kind: KindFileRef, Name: "shop/app/templates/index.hbs", File: "shop/app/templates/index.hbs", Repo: "shop",
			Props: map[string]any{"language": "handlebars"},
			Relations: []Relation{
				{Kind: RelCalls, Target: "catalog"},
				{Kind: RelCalls, Target: "account"},
			}},
	)
	got := writtenTargetIDs(t, s)
	for key, want := range map[string]string{
		"shop/app/templates/index.hbs|calls|catalog": KindRoute,
		"shop/app/templates/index.hbs|calls|account": "",
	} {
		if got[key] != want {
			t.Errorf("%s: target_id names a %q, want %q", key, got[key], want)
		}
	}
}
