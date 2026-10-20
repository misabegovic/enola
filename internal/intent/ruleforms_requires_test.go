package intent

import (
	"strings"
	"testing"
)

// requiresFill writes a valid value for one key a form requires, the way a
// declaration author would.
var requiresFill = map[string]func(*ConstraintRule){
	"to":                func(r *ConstraintRule) { r.To = "b" },
	"to_name":           func(r *ConstraintRule) { r.ToName = []string{"External*"} },
	"via":               func(r *ConstraintRule) { r.Via = "calls" },
	"only":              func(r *ConstraintRule) { r.Only = []string{"b"} },
	"owners":            func(r *ConstraintRule) { r.Owners = []string{"b"} },
	"max_members":       func(r *ConstraintRule) { r.MaxMembers = 1 },
	"must_prop_contain": func(r *ConstraintRule) { r.MustPropContain = &PropMatch{Prop: "columns", Value: "id"} },
	"direction":         func(r *ConstraintRule) { r.Direction = "inbound" },
	"method":            func(r *ConstraintRule) { r.Method = "call" },
	"any_of":            func(r *ConstraintRule) { r.AnyOf = []string{"call", "perform"} },
	"among":             func(r *ConstraintRule) { r.Among = []string{"b"} },
	"pattern":           func(r *ConstraintRule) { r.Pattern = "*Job" },
	"steps":             func(r *ConstraintRule) { r.Steps = []string{"b", "c"} },
	"message":           func(r *ConstraintRule) { r.Message = "prefer the adapter" },
	"metric":            func(r *ConstraintRule) { r.Metric = "queries" },
	"max":               func(r *ConstraintRule) { r.Max = 1 },
	"by":                func(r *ConstraintRule) { r.By = "table" },
}

// RuleForm.Requires states what the validator refuses a rule for leaving out.
// For every form: a rule carrying one key of each group validates clean, a
// rule missing any one group is refused, and every alternative in a group
// satisfies it alone. A form whose validation changes without its Requires,
// or the reverse, fails here.
func TestRuleFormRequiresIsWhatValidationRefuses(t *testing.T) {
	components := []ConstraintComponent{
		{Name: "a", Match: []string{"a/**"}},
		{Name: "b", Match: []string{"b/**"}},
		{Name: "c", Match: []string{"c/**"}},
	}
	build := func(form RuleForm, keys map[int]string, skip int) ConstraintRule {
		r := ConstraintRule{ID: "r", Because: "a rule with no rationale surfaces none"}
		form.Set(&r, "a")
		for gi, group := range form.Requires {
			if gi == skip {
				continue
			}
			key := group[0]
			if k, ok := keys[gi]; ok {
				key = k
			}
			fill, ok := requiresFill[key]
			if !ok {
				t.Fatalf("%s requires %q and the test has no value for it", form.Key, key)
			}
			fill(&r)
		}
		return r
	}
	for _, form := range RuleForms {
		if problems := constraintProblems(components, []ConstraintRule{build(form, nil, -1)}); len(problems) > 0 {
			t.Errorf("%s with every required group is refused: %s", form.Key, strings.Join(problems, "; "))
		}
		for gi, group := range form.Requires {
			if problems := constraintProblems(components, []ConstraintRule{build(form, nil, gi)}); len(problems) == 0 {
				t.Errorf("%s without %s validates clean, so Requires names a key the validator does not demand", form.Key, strings.Join(group, " or "))
			}
			for _, alt := range group[1:] {
				if problems := constraintProblems(components, []ConstraintRule{build(form, map[int]string{gi: alt}, -1)}); len(problems) > 0 {
					t.Errorf("%s with %s in place of %s is refused: %s", form.Key, alt, group[0], strings.Join(problems, "; "))
				}
			}
		}
	}
}
