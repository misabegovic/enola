package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// structured calls a tool and returns its structured content as generic JSON.
func structured(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	if res.IsError {
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		t.Fatalf("%s %v refused: %s", tool, args, sb.String())
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: structured content is not an object: %v\n%s", tool, err, raw)
	}
	return out
}

// withoutTiming drops the fields that say when something ran, which two
// processes reading one snapshot may stamp differently.
func withoutTiming(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if strings.Contains(k, "generated_at") || strings.Contains(k, "duration") {
				continue
			}
			out[k] = withoutTiming(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = withoutTiming(val)
		}
		return out
	}
	return v
}

func sameAnswer(t *testing.T, label string, tool map[string]any, cli string) {
	t.Helper()
	want := withoutTiming(decodeJSON(t, label+" (command line)", cli))
	if got := withoutTiming(tool); !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("%s: the tool and the command line answer differently\ntool:\n%s\ncommand line:\n%s", label, gotJSON, wantJSON)
	}
}

// The ledger, lint and grade answer as tools exactly what their command-line
// verbs print as JSON, over one snapshot; and after a one-line edit, grade
// still answers what check does.
func TestTheNewToolsAnswerWhatTheirVerbsPrint(t *testing.T) {
	a := newAnswersRun(t)
	a.mustGenerate()
	if code, out, errOut := a.run("baseline", "pin", "."); code != 0 {
		t.Fatalf("pin: exit %d\n%s\n%s", code, out, errOut)
	}
	cs := toolSession(t, a.home, a.repo)

	for _, c := range []struct {
		label string
		tool  string
		args  map[string]any
		cli   []string
	}{
		{"ledger", "constraints_ledger", map[string]any{}, []string{"constraints", "ledger", "--json", "."}},
		{"ledger with breaches", "constraints_ledger", map[string]any{"breaches": true}, []string{"constraints", "ledger", "--json", "--breaches", "."}},
		{"lint", "constraints_lint", map[string]any{}, []string{"constraints", "lint", "--json", "."}},
		{"grade", "grade", map[string]any{"fail_on": []string{"constraints"}}, []string{"check", "--json", "--fail-on=constraints", "."}},
	} {
		_, out, errOut := a.run(c.cli...)
		if !strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Fatalf("%s: the command line printed no document\n%s\n%s", c.label, out, errOut)
		}
		sameAnswer(t, c.label, structured(t, cs, c.tool, c.args), out)
	}

	api := filepath.Join(a.repo, "internal", "api", "api.go")
	if err := os.WriteFile(api, []byte("package api\n\nimport \"example.com/r/internal/store\"\n\nfunc Serve() { store.Put1(); store.Put2() }\n\nfunc Again() { store.Put1() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, errOut := a.run("check", "--json", "--fail-on=constraints", ".")
	if !strings.Contains(out, `"regression"`) {
		t.Fatalf("the edit was meant to regress check:\n%s\n%s", out, errOut)
	}
	sameAnswer(t, "grade after an edit", structured(t, cs, "grade", map[string]any{"fail_on": []string{"constraints"}}), out)
}
