package rubyextractor

import (
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func TestSequelModelBase(t *testing.T) {
	cases := []struct {
		super, table string
		ok           bool
	}{
		{"Sequel::Model", "", true},
		{"Sequel::Model(:customers)", "customers", true},
		{"Sequel::Model(db[:x])", "", true},
		{"ApplicationRecord", "", false},
	}
	for _, c := range cases {
		table, ok := sequelModelBase(c.super)
		if table != c.table || ok != c.ok {
			t.Errorf("sequelModelBase(%q) = %q,%v want %q,%v", c.super, table, ok, c.table, c.ok)
		}
	}
}

// TestExplicitTableNameCorrectsModelFact: `self.table_name = "…"` must become
// the model's own storage truth. The defect this pins: the declared name was
// extracted as a second, standalone table fact while the model's fact kept the
// convention-derived name — a table that does not exist — so every join from
// model to physical table silently resolved to the wrong one, with the
// correction sitting beside it looking like additional information.
func TestExplicitTableNameCorrectsModelFact(t *testing.T) {
	src := []byte("class LegacyThing < ApplicationRecord\n  self.table_name = \"old_things\"\nend\n")
	ff := extractFileAST(src, "app/models/legacy_thing.rb", true, true)
	var model *facts.Fact
	for i := range ff {
		if ff[i].Kind != facts.KindStorage {
			continue
		}
		if ff[i].Name == "LegacyThing" {
			model = &ff[i]
			continue
		}
		t.Errorf("unexpected standalone storage fact %q — the declared table corrects the model's fact, it never becomes a fact of its own", ff[i].Name)
	}
	if model == nil {
		t.Fatal("model emitted no storage fact")
	}
	if model.Props["table"] != "old_things" {
		t.Fatalf("model table = %v, want the declared old_things, not the derived legacy_things", model.Props["table"])
	}
	if model.Props["table_source"] != "declared" {
		t.Fatalf("table_source = %v, want declared — a stated name is not a convention holding", model.Props["table_source"])
	}
}

func TestSequelModelDatasetForm_ThroughAST(t *testing.T) {
	src := []byte("class CustomerRecord < Sequel::Model(:customers)\n  def display_name\n    name.upcase\n  end\nend\n")
	ff := extractFileAST(src, "app/models/customer_record.rb", true, true)
	var storage *facts.Fact
	for i := range ff {
		if ff[i].Kind == facts.KindStorage && ff[i].Name == "CustomerRecord" {
			storage = &ff[i]
		}
		if ff[i].Kind == facts.KindSymbol && ff[i].Name == "CustomerRecord" {
			if ff[i].Props["superclass"] != "Sequel::Model" {
				t.Fatalf("superclass prop = %v, want the base name without call arguments", ff[i].Props["superclass"])
			}
		}
	}
	if storage == nil {
		t.Fatal("the dataset-form Sequel model emitted no storage fact — the call-form superclass was dropped (the miss the ruby_sample golden pinned until v154)")
	}
	if storage.Props["table"] != "customers" || storage.Props["framework"] != "sequel" {
		t.Fatalf("storage fact = %+v, want table=customers framework=sequel", storage.Props)
	}
}
