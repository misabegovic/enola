package rubyextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

var accessRepo = map[string]string{
	"Gemfile":               "source \"https://rubygems.org\"\n",
	"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
	"app/models/account.rb": `class Account < ApplicationRecord
  has_many :entries

  def self.open_for(owner)
    find_by(owner: owner)
  end

  def self.claim(name)
    find_or_create_by(name: name)
  end

  def close!
    update!(closed: true)
    entries.update_all(closed: true)
  end
end
`,
	"app/models/entry.rb": `class Entry < ApplicationRecord
  belongs_to :account
  has_many :lines
end
`,
	"app/models/line.rb": `class Line < ApplicationRecord
  belongs_to :entry
end
`,
	"app/services/ledger.rb": `class Ledger
  def balance(account)
    Account.find(account.id)
    account.entries.count
    Entry.create!(account: account)
  end

  def tidy(params)
    lines.delete_all
    options = {}
    options.delete(:x)
    params.delete(:y)
    Rails.cache.delete("k")
    save!
  end
end
`,
}

// accessEdges lists every reads and writes relation as "Source kind Target".
func accessEdges(ff []facts.Fact) []string {
	var out []string
	for _, f := range ff {
		for _, rel := range f.Relations {
			if rel.Kind == facts.RelReads || rel.Kind == facts.RelWrites {
				out = append(out, f.Name+" "+rel.Kind+" "+rel.Target)
			}
		}
	}
	sort.Strings(out)
	return out
}

// A method reads or writes a model's table only where the receiver names the
// model exactly: its class, no receiver inside the model's own class, or an
// association that model itself declares. find_or_create_by does both.
func TestRubyAccess_ExactReceiversEmitReadsAndWrites(t *testing.T) {
	got := accessEdges(extractRepo(t, accessRepo))
	want := []string{
		"Account#close! writes Account",
		"Account#close! writes Entry",
		"Account.claim reads Account",
		"Account.claim writes Account",
		"Account.open_for reads Account",
		"Ledger#balance reads Account",
		"Ledger#balance writes Entry",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("access edges =\n%v\nwant\n%v", got, want)
	}
}

// Every verb call that resolves to no model exactly emits nothing and is
// counted by its cause: a chain, an association another model declares, a
// local or a parameter, and a receiverless call outside a model.
func TestRubyAccess_UnresolvedCallsAreCountedByCause(t *testing.T) {
	ff := extractRepo(t, accessRepo)
	record, ok := extractionFact(ff, "ruby:access")
	if !ok {
		t.Fatalf("no ruby:access record: %+v", ff)
	}
	if got := record.Props["unresolved_macros"]; got != "association_not_declared_by_owner=1,chained_receiver=2,local_receiver=2,receiverless_outside_model=1" {
		t.Fatalf("unresolved_macros = %v", got)
	}
	entry := record.Props["edge_coverage"].([]map[string]any)[0]
	if entry["edge_type"] != "ruby_access" || entry["resolved"] != 6 || entry["unresolved"] != 6 {
		t.Fatalf("edge_coverage = %+v, want 6 calls resolved and 6 left out", entry)
	}
}

// A class that is not a model, a module and a model that defines the verb itself
// resolve nothing: the first two because their constant names no table, the
// third because the call reaches the model's own method rather than the one
// ActiveRecord gives it.
func TestRubyAccess_OtherConstantsAndOwnVerbsAreRefused(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"Gemfile":               "source \"https://rubygems.org\"\n",
		"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
		"app/models/account.rb": `class Account < ApplicationRecord
  def self.sum(values)
    values.reduce(:+)
  end

  def total
    sum([1, 2])
  end
end
`,
		"app/services/report.rb": `class Report
  def run
    Account.sum([1])
    Redis.count
    Report.find(1)
  end
end
`,
	})
	if got := accessEdges(ff); len(got) != 0 {
		t.Fatalf("no call here names a table, got %v", got)
	}
	record, ok := extractionFact(ff, "ruby:access")
	if !ok {
		t.Fatal("no ruby:access record")
	}
	if got := record.Props["unresolved_macros"]; got != "other_constant=2,verb_defined_by_model=2" {
		t.Fatalf("unresolved_macros = %v", got)
	}
}

// A repository that declares no model has no table to name, so it reports no
// access at all rather than a census of every array's count.
func TestRubyAccess_NoModelNoRecord(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"lib/tally.rb": "class Tally\n  def run(items)\n    items.count\n    Tally.find(1)\n  end\nend\n",
	})
	if got := accessEdges(ff); len(got) != 0 {
		t.Fatalf("no model, no edge, got %v", got)
	}
	if _, ok := extractionFact(ff, "ruby:access"); ok {
		t.Fatal("a repository with no model must not report an access census")
	}
}

// The write list is the cqrs recipe's persistence list, word for word, so the
// rule that forbids a query from writing and the edge that says it writes cannot
// come to mean two different things.
func TestRubyAccess_WriteListIsTheCqrsRecipes(t *testing.T) {
	recipes, problems := intent.BuiltinRecipes()
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	var recipeWrites []string
	for _, rec := range recipes {
		if rec.Name != "cqrs" {
			continue
		}
		for _, r := range rec.Rules {
			if r.ID == "queries-never-write" {
				recipeWrites = append(recipeWrites, r.ToName...)
			}
		}
	}
	if len(recipeWrites) == 0 {
		t.Fatal("the cqrs recipe carries no queries-never-write list")
	}
	if !reflect.DeepEqual(accessWrites, recipeWrites) {
		t.Fatalf("accessWrites = %v\nrecipe       = %v", accessWrites, recipeWrites)
	}
	for _, verb := range accessBoth {
		if accessVerbs[verb] != accessReadWrite {
			t.Fatalf("%s must read and write", verb)
		}
	}
}
