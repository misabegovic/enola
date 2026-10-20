package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// smallToolNames is the --small profile, sorted. Serving a tool beyond these, or
// losing one, fails here: the profile is a list someone decided, not whatever
// happens to be registered.
var smallToolNames = []string{
	"constraints_for", "context_pack", "explore", "governing_intent", "impact_analysis", "query_insights",
}

// defaultMaxTokensText is what every tool on the default server says about
// max_tokens, spelled out here so a tag that drifts from the others fails.
const defaultMaxTokensText = "Cap on output size in approximate tokens (four characters each); " +
	"output over it is cut on a line boundary with a notice. Default: 8000. A negative value means no cap."

// connectProfile is connect with server options, returning the session and the
// instructions the client received.
func connectProfile(t *testing.T, eng *bootstrap.Engine, cfg *config.Config, opts ...bootstrap.ServerOption) (*session, string) {
	t.Helper()
	ctx := context.Background()
	srv, err := bootstrap.NewServer(eng, cfg, opts...)
	if err != nil {
		t.Fatalf("bootstrap.NewServer: %v", err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "enola-test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	instructions := ""
	if init := cs.InitializeResult(); init != nil {
		instructions = init.Instructions
	}
	return &session{cs: cs}, instructions
}

func listTools(t *testing.T, s *session) []*mcp.Tool {
	t.Helper()
	list, err := s.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	return list.Tools
}

func maxTokensText(t *testing.T, tool *mcp.Tool) string {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	prop, ok := schema.Properties["max_tokens"]
	if !ok {
		return ""
	}
	if prop.Type != "integer" {
		t.Errorf("%s: max_tokens is %v, want integer", tool.Name, prop.Type)
	}
	return prop.Description
}

func TestSmallProfile_ServesItsListAndNothingElse(t *testing.T) {
	eng, cfg := newTestEngine(t)
	small, instructions := connectProfile(t, eng, cfg, bootstrap.SmallProfile())
	_, fullInstructions := connectProfile(t, eng, cfg)

	tools := listTools(t, small)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		if strings.Contains(tool.Description, "\n") || len(tool.Description) > 120 {
			t.Errorf("%s: the small description is not one short line: %q", tool.Name, tool.Description)
		}
		if got := maxTokensText(t, tool); !strings.Contains(got, "Default: 2000") || !strings.Contains(got, "negative value means no cap") {
			t.Errorf("%s: max_tokens says %q, want the small default and the negative rule", tool.Name, got)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, smallToolNames) {
		t.Fatalf("--small serves %v, want %v", names, smallToolNames)
	}

	for _, name := range smallToolNames {
		if !strings.Contains(instructions, name) {
			t.Errorf("the small instructions do not name %s:\n%s", name, instructions)
		}
	}
	for _, name := range expectedTools {
		if !slices.Contains(smallToolNames, name) && regexp.MustCompile(`\b`+name+`\b`).MatchString(instructions) {
			t.Errorf("the small instructions name %s, which --small does not serve:\n%s", name, instructions)
		}
	}
	if len(instructions) >= len(fullInstructions)/2 {
		t.Errorf("the small instructions are %d bytes against the default's %d; they are meant to be short",
			len(instructions), len(fullInstructions))
	}

	res, err := small.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_snapshot", Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("generate_snapshot answered under --small:\n%s", text(res))
	}
}

func TestDefaultServer_EveryToolTakesMaxTokens(t *testing.T) {
	eng, cfg := newTestEngine(t)
	s, _ := connectProfile(t, eng, cfg)
	tools := listTools(t, s)
	if len(tools) != 26 {
		t.Errorf("the default server lists %d tools, want 26", len(tools))
	}
	for _, tool := range tools {
		if got := maxTokensText(t, tool); got != defaultMaxTokensText {
			t.Errorf("%s: max_tokens description = %q\nwant %q", tool.Name, got, defaultMaxTokensText)
		}
	}
}

// The nine tools that gained the parameter accept it on the wire: an argument the
// schema did not declare is refused before the tool runs.
func TestDefaultServer_TheNineNewMaxTokensArePassedBySchema(t *testing.T) {
	s := startInMemory(t)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"generate_snapshot", map[string]any{"repo_path": s.repo}},
		{"show_symbol", map[string]any{"name": "Alpha"}},
		{"find_path", map[string]any{"from": "pkg/a", "to": "pkg/b"}},
		{"endpoint_impact", map[string]any{"endpoint": "/nothing"}},
		{"context_pack", map[string]any{"paths": []string{"pkg/a"}, "budget": 100}},
		{"coverage_report", map[string]any{}},
		{"set_baseline", map[string]any{}},
		{"architecture_history", map[string]any{}},
		{"architecture_blame", map[string]any{"pattern": "pkg/a"}},
	} {
		c.args["max_tokens"] = -1
		res := s.call(t, c.tool, c.args)
		if got := text(res); strings.Contains(got, "validating") || strings.Contains(got, "additional") {
			t.Errorf("%s refused max_tokens: %s", c.tool, got)
		}
	}
}

// wideRepo writes a Go module with enough symbols that a query_facts page of 500
// is several times the default cap.
func wideRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "wide")
	writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/wide\n\ngo 1.25\n")
	for p := 0; p < 12; p++ {
		var src bytes.Buffer
		fmt.Fprintf(&src, "package p%d\n\n", p)
		for f := 0; f < 50; f++ {
			fmt.Fprintf(&src, "// Function%dOfPackage%d does nothing of note.\nfunc Function%dOfPackage%d() int { return %d }\n\n", f, p, f, p, f)
		}
		writeTestFile(t, filepath.Join(repo, fmt.Sprintf("p%d", p), "p.go"), src.String())
	}
	return repo
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultCap_OverTheWire(t *testing.T) {
	// A home of its own: the workspace receipt another test in this package wrote
	// would otherwise reach this server as another session's graph, and its notice
	// would lead the first answer and nothing after it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	eng, cfg := newTestEngine(t)
	repo := wideRepo(t)
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatalf("GenerateSnapshot: %v", err)
	}
	s, _ := connectProfile(t, eng, cfg)
	query := func(extra map[string]any) string {
		args := map[string]any{"kind": "symbol", "limit": 500}
		for k, v := range extra {
			args[k] = v
		}
		return text(s.call(t, "query_facts", args))
	}

	whole := query(map[string]any{"max_tokens": -1})
	if len(whole) <= 8000*4 {
		t.Fatalf("the fixture's page is %d characters, under the default cap; the test proves nothing", len(whole))
	}
	if strings.Contains(whole, "[truncated") {
		t.Errorf("a negative max_tokens was cut")
	}
	if got := query(map[string]any{"max_tokens": 10_000_000}); got != whole {
		t.Errorf("a negative max_tokens differs from a cap nothing reaches: %d vs %d characters", len(whole), len(got))
	}

	omitted := query(nil)
	if !strings.Contains(omitted, "max_tokens=8000") || len(omitted) >= len(whole) {
		t.Errorf("an omitted max_tokens was not cut at the default: %d characters, notice present=%v",
			len(omitted), strings.Contains(omitted, "max_tokens=8000"))
	}
	if !strings.HasPrefix(whole, strings.SplitN(omitted, "\n\n[truncated", 2)[0]) {
		t.Errorf("the default cut is not a prefix of the whole result")
	}

	if got := query(map[string]any{"max_tokens": 500}); !strings.Contains(got, "max_tokens=500") || strings.Count(got, "[truncated") != 1 {
		t.Errorf("an explicit max_tokens=500 did not win with one notice:\n%s", got[len(got)-300:])
	}

	// Under the cap, the default server's answer is what the tool returns.
	shortArgs := map[string]any{"kind": "module"}
	short := text(s.call(t, "query_facts", shortArgs))
	shortArgs["max_tokens"] = -1
	if uncapped := text(s.call(t, "query_facts", shortArgs)); short != uncapped || len(short) > 8000*4 {
		t.Errorf("a result under the cap changed: %d characters capped, %d uncapped", len(short), len(uncapped))
	}
}

func TestSmallProfile_CompactJSONAndItsCap(t *testing.T) {
	// A home of its own: the workspace receipt another test in this package wrote
	// would otherwise reach this server as another session's graph, and its notice
	// would lead the first answer and nothing after it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	eng, cfg := newTestEngine(t)
	repo := wideRepo(t)
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatalf("GenerateSnapshot: %v", err)
	}
	full, _ := connectProfile(t, eng, cfg)
	small, _ := connectProfile(t, eng, cfg, bootstrap.SmallProfile())

	args := map[string]any{"output_mode": "full", "max_tokens": -1}
	indented := text(full.call(t, "query_insights", args))
	compact := text(small.call(t, "query_insights", args))
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(indented)); err != nil {
		t.Fatalf("the default server's query_insights is not JSON: %v\n%s", err, indented)
	}
	if compact != want.String() {
		t.Errorf("--small query_insights is not the default server's result compacted:\n%s\nwant\n%s", compact, want.String())
	}
	if len(compact) <= 2000*4 {
		t.Fatalf("the fixture's insights are %d characters compacted, under the small cap; the rest proves nothing", len(compact))
	}

	got := text(small.call(t, "query_insights", map[string]any{"output_mode": "full"}))
	if !strings.Contains(got, "max_tokens=2000") || !strings.Contains(got, "no longer valid JSON") {
		t.Errorf("--small did not cut full JSON at its default with the JSON notice:\n%s", got[len(got)-300:])
	}
	if !strings.HasPrefix(compact, strings.SplitN(got, "\n\n[truncated", 2)[0]) {
		t.Errorf("the small cut is not a prefix of the compacted whole: it was cut before it was compacted")
	}

	// A caller's own max_tokens is the case the tool would cut itself, in indented
	// JSON that could no longer be compacted.
	asked := text(small.call(t, "query_insights", map[string]any{"output_mode": "full", "max_tokens": 1000}))
	if !strings.Contains(asked, "max_tokens=1000") || !strings.HasPrefix(compact, strings.SplitN(asked, "\n\n[truncated", 2)[0]) {
		t.Errorf("--small with max_tokens=1000 is not the compacted whole cut at 1000:\n%s", asked[:min(len(asked), 300)])
	}

	eng2, cfg2 := newTestEngine(t)
	empty, _ := connectProfile(t, eng2, cfg2, bootstrap.SmallProfile())
	res := empty.call(t, "explore", map[string]any{"focus": "p3"})
	if !res.IsError || !strings.Contains(text(res), "--generate") {
		t.Errorf("--small with no snapshot did not name the command that makes one:\n%s", text(res))
	}
}
