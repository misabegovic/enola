package server_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/wireschema"
)

// typedTools answer with a value a program reads as well as their text: the
// tools a caller keeping one server up reads, and the small profile's six.
var typedTools = []string{
	"constraints_ledger", "constraints_lint", "grade",
	"plan_check", "diff_snapshot", "snapshot_receipt",
	"query_facts", "impact_analysis", "find_path", "traverse",
	"constraints_for", "context_pack", "explore", "governing_intent", "query_insights",
}

func asObject(t *testing.T, label string, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not a JSON object: %v\n%s", label, err, raw)
	}
	return out
}

// lawSession snapshots alpha, whose law api-avoids-store its own api breaks,
// and pins it, so every typed tool has something to answer.
func lawSession(t *testing.T) (*session, string) {
	t.Helper()
	alpha := writeRepo(t, "alpha", map[string]string{
		"go.mod":                  "module alpha\n\ngo 1.21\n",
		"internal/store/store.go": "package store\n\nfunc Put() {}\n",
		"internal/api/api.go":     "package api\n\nimport \"alpha/internal/store\"\n\nfunc Serve() { store.Put() }\n",
		"enola-intent.yaml":       alphaLaw,
	})
	eng, cfg := newTestEngine(t)
	s := connect(t, eng, cfg)
	s.repo = alpha
	s.snapshot(t)
	if res := s.call(t, "set_baseline", map[string]any{}); res.IsError {
		t.Fatalf("set_baseline: %s", text(res))
	}
	return s, alpha
}

// Every typed tool lists an output schema, and on every mode it answers in its
// structured content validates against that schema, strictly: a field the
// encoder writes and the schema does not name fails, so the schema describes
// the whole answer.
func TestTypedToolsListASchemaAndAnswerWithinIt(t *testing.T) {
	s, _ := lawSession(t)

	schemas := map[string]map[string]any{}
	for _, tool := range listTools(t, s) {
		if tool.OutputSchema != nil {
			schemas[tool.Name] = asObject(t, tool.Name+" output schema", tool.OutputSchema)
		}
	}
	for _, name := range typedTools {
		if schemas[name] == nil {
			t.Errorf("%s lists no output schema", name)
		}
	}

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"query_facts", map[string]any{"kind": "symbol"}},
		{"query_facts", map[string]any{"kind": "symbol", "limit": 1}},
		{"query_facts", map[string]any{"kind": "symbol", "output_mode": "summary"}},
		{"query_facts", map[string]any{"kind": "service"}},
		{"impact_analysis", map[string]any{"target": "internal/store.Put"}},
		{"impact_analysis", map[string]any{"target": "internal/store.Put", "output_mode": "full"}},
		{"impact_analysis", map[string]any{"target": "internal/store.Put", "output_mode": "compact"}},
		{"traverse", map[string]any{"start": "internal/api.Serve"}},
		{"traverse", map[string]any{"start": "internal/api.Serve", "output_mode": "full"}},
		{"find_path", map[string]any{"from": "internal/api.Serve", "to": "internal/store.Put"}},
		{"find_path", map[string]any{"from_nodes": []string{"internal/api.Serve"}, "to_nodes": []string{"internal/store.Put", "nowhere"}}},
		{"explore", map[string]any{"focus": "internal/api"}},
		{"governing_intent", map[string]any{"target": "internal/api/api.go"}},
		{"constraints_for", map[string]any{"target": "internal/api/api.go"}},
		{"context_pack", map[string]any{"paths": []string{"internal/api/api.go"}, "budget": 2000}},
		{"query_insights", map[string]any{}},
		{"query_insights", map[string]any{"output_mode": "full"}},
		{"query_insights", map[string]any{"explainer": "no-such-explainer"}},
		{"plan_check", map[string]any{"paths": []string{"internal/api/api.go"}}},
		{"diff_snapshot", map[string]any{}},
		{"diff_snapshot", map[string]any{"output_mode": "full"}},
		{"snapshot_receipt", map[string]any{}},
		{"constraints_ledger", map[string]any{}},
		{"constraints_ledger", map[string]any{"breaches": true}},
		{"constraints_lint", map[string]any{}},
		{"grade", map[string]any{}},
		{"grade", map[string]any{"fail_on": []string{"constraints"}, "format": "json"}},
	}
	for _, c := range calls {
		label := fmt.Sprintf("%s %v", c.tool, c.args)
		res := s.call(t, c.tool, c.args)
		if res.IsError {
			t.Errorf("%s: %s", label, text(res))
			continue
		}
		if res.StructuredContent == nil {
			t.Errorf("%s: no structured content", label)
			continue
		}
		schema := schemas[c.tool]
		if schema == nil {
			continue
		}
		doc := asObject(t, label, res.StructuredContent)
		if errs := wireschema.Validate(schema, doc, true); len(errs) > 0 {
			t.Errorf("%s: structured content departs from the schema:\n  %s", label, strings.Join(errs, "\n  "))
		}
	}
}

// The cap cuts what a reader reads, never what a program reads: a full answer
// over the default cap arrives cut in its text and whole in its structured
// content.
func TestStructuredContentIsWholeOverTheCap(t *testing.T) {
	var body strings.Builder
	body.WriteString("package big\n\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&body, "func Function%03d() {}\n\n", i)
	}
	repo := writeRepo(t, "big", map[string]string{
		"go.mod":         "module big\n\ngo 1.21\n",
		"big/big.go":     body.String(),
		"cmd/big/big.go": "package main\n\nfunc main() {}\n",
	})
	eng, cfg := newTestEngine(t)
	s := connect(t, eng, cfg)
	s.repo = repo
	s.snapshot(t)

	res := s.call(t, "query_facts", map[string]any{"kind": "symbol", "limit": 500})
	if res.IsError {
		t.Fatal(text(res))
	}
	if !strings.Contains(text(res), "[truncated: JSON output exceeded max_tokens=8000") {
		t.Fatalf("the text was not cut at the default cap (%d bytes)", len(text(res)))
	}
	var answer struct {
		Facts []json.RawMessage `json:"facts"`
		Total int               `json:"total"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the structured content is not whole JSON: %v", err)
	}
	if len(answer.Facts) != 500 || answer.Total < 600 {
		t.Errorf("structured content holds %d facts of %d, want all 500 the call asked for", len(answer.Facts), answer.Total)
	}
}

// impact's summary reads the same on every call over one snapshot, so a caller
// that hashes or caches answers can.
func TestImpactSummaryIsOneTextAcrossTwelveCalls(t *testing.T) {
	s, _ := lawSession(t)
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		res := s.call(t, "impact_analysis", map[string]any{"target": "internal/store.Put", "output_mode": "full", "max_tokens": -1})
		if res.IsError {
			t.Fatal(text(res))
		}
		seen[text(res)] = true
	}
	if len(seen) != 1 {
		t.Errorf("12 identical impact calls gave %d distinct texts", len(seen))
	}
}
