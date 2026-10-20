package intent

import (
	"reflect"
	"strings"
	"testing"
)

// reads and writes are edge kinds a rule may name, and only on the forms that
// judge one edge at a time. A table is a valid far end, including one selected
// by its table prop, since a storage fact has no methods for owns: to speak about.
func TestAccessVias_TheEdgeFormsNameThem(t *testing.T) {
	billing := ConstraintComponent{Name: "billing", Match: []string{"app/billing/**"}}
	accounts := ConstraintComponent{Name: "accounts", Kind: "storage", Where: map[string]any{"table": "accounts"}}
	models := ConstraintComponent{Name: "models", Match: []string{"app/models/**"}, Kind: "storage"}
	for name, rule := range map[string]ConstraintRule{
		"forbid via writes to a table selected by its prop": {ID: "r", Forbid: "billing", To: "accounts", Via: "writes", Because: "x"},
		"forbid via reads to a table selected by its path":  {ID: "r", Forbid: "billing", To: "models", Via: "reads", Because: "x"},
		"allow only via writes":                             {ID: "r", Allow: "billing", Only: []string{"models"}, Via: "writes", Because: "x"},
		"protect via writes":                                {ID: "r", Protect: "models", Owners: []string{"billing"}, Via: "writes", Because: "x"},
		"forbid via writes to a literal model name":         {ID: "r", Forbid: "billing", ToName: []string{"Account"}, Via: "writes", Because: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := constraintDecl([]ConstraintComponent{billing, accounts, models}, []ConstraintRule{rule}).Validate(); err != nil {
				t.Fatalf("want valid, got %v", err)
			}
		})
	}
}

func TestAccessVias_TheWalkingFormsRefuseThem(t *testing.T) {
	billing := ConstraintComponent{Name: "billing", Match: []string{"app/billing/**"}}
	models := ConstraintComponent{Name: "models", Match: []string{"app/models/**"}, Kind: "storage"}
	accounts := ConstraintComponent{Name: "accounts", Kind: "storage", Where: map[string]any{"table": "accounts"}}
	for name, tc := range map[string]struct {
		rule   ConstraintRule
		wantIn string
	}{
		"forbid_reach walks, and an access edge ends the walk": {ConstraintRule{ID: "r", ForbidReach: "billing", To: "models", Via: "writes", Because: "x"}, "only forbid, allow and protect"},
		"require_edge": {ConstraintRule{ID: "r", RequireEdge: "billing", To: "models", Via: "writes", Direction: "outbound", Because: "x"}, "only forbid, allow and protect"},
		"protocol":     {ConstraintRule{ID: "r", Protocol: "billing", Steps: []string{"billing", "models"}, Via: "reads", Because: "x"}, "only forbid, allow and protect"},
		"when_edge_to": {ConstraintRule{ID: "r", Require: "billing", MustPropContain: &PropMatch{Prop: "a", Value: "b"}, WhenEdgeTo: []string{"Account"}, Via: "writes", Because: "x"}, "when_edge_to needs a via"},
		"a table by its prop under a code edge still states what it owns": {ConstraintRule{ID: "r", Forbid: "billing", To: "accounts", Via: "calls", Because: "x"}, "nothing declares what it owns"},
	} {
		t.Run(name, func(t *testing.T) {
			err := constraintDecl([]ConstraintComponent{billing, models, accounts}, []ConstraintRule{tc.rule}).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("want an error naming %q, got %v", tc.wantIn, err)
			}
		})
	}
}

// The forms that walk every rule-via kind walk the code kinds only, as before:
// a declaration that names no access via reads the graph it always did.
func TestAccessVias_WholeVocabularyWalksStayOnCode(t *testing.T) {
	code := []string{"calls", "depends_on", "implements", "imports"}
	for _, r := range []ConstraintRule{
		{Private: "billing"},
		{Independent: "billing"},
		{ForbidReach: "billing", To: "models"},
	} {
		if got := RuleVias(r); !reflect.DeepEqual(got, code) {
			t.Fatalf("RuleVias(%+v) = %v, want %v", r, got, code)
		}
	}
	if !AllowedRuleVias["reads"] || !AllowedRuleVias["writes"] || !IsAccessVia("writes") || IsAccessVia("calls") {
		t.Fatal("reads and writes are in the vocabulary and are the access kinds")
	}
}
