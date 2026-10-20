package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// big is well over the default server's 8,000-token cap (32,000 characters) and
// under 20,000 tokens, so an explicit cap between the two can be told apart from
// the default one.
var big = strings.Repeat("abcdefghij\n", 4000)

func capCall(t *testing.T, s *Server, args string, result *mcp.CallToolResult) *mcp.CallToolResult {
	t.Helper()
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) { return result, nil }
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{
		Name: "query_facts", Arguments: json.RawMessage(args),
	}}
	out, err := s.capMiddleware(handler)(context.Background(), "tools/call", req)
	if err != nil {
		t.Fatalf("capMiddleware: %v", err)
	}
	return out.(*mcp.CallToolResult)
}

func textOf(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func TestCapMiddleware_OmittedValueTakesTheProfileDefault(t *testing.T) {
	for _, c := range []struct {
		name  string
		small bool
		cap   int
	}{{"default server", false, defaultMaxTokens}, {"small profile", true, smallMaxTokens}} {
		t.Run(c.name, func(t *testing.T) {
			for _, args := range []string{``, `{}`, `{"max_tokens":0}`, `{"max_tokens":null}`} {
				got := text(capCall(t, &Server{small: c.small}, args, textOf(big)))
				if want := capTokens(big, c.cap, false); got != want {
					t.Fatalf("arguments %q: got %d characters, want the %d-token default cut (%d characters)",
						args, len(got), c.cap, len(want))
				}
				if !strings.Contains(got, "max_tokens=") || !strings.Contains(got, "negative max_tokens") {
					t.Errorf("arguments %q: the cut carries no notice saying how to ask for all of it:\n%s",
						args, got[len(got)-300:])
				}
			}
		})
	}
}

func TestCapMiddleware_NegativeValueReturnsTheToolsBytes(t *testing.T) {
	for _, small := range []bool{false, true} {
		for _, args := range []string{`{"max_tokens":-1}`, `{"max_tokens":-50000}`} {
			if got := text(capCall(t, &Server{small: small}, args, textOf(big))); got != big {
				t.Errorf("small=%v %s: got %d characters, want the tool's %d unchanged", small, args, len(got), len(big))
			}
		}
	}
}

func TestCapMiddleware_ExplicitValueWins(t *testing.T) {
	s := &Server{}
	if got, want := text(capCall(t, s, `{"max_tokens":500}`, textOf(big))), capTokens(big, 500, false); got != want {
		t.Errorf("max_tokens=500: got %d characters, want %d", len(got), len(want))
	}
	if got := text(capCall(t, s, `{"max_tokens":20000}`, textOf(big))); got != big {
		t.Errorf("max_tokens=20000 above the default: got %d characters, want all %d", len(got), len(big))
	}
	small := &Server{small: true}
	if got := text(capCall(t, small, `{"max_tokens":8000}`, textOf(big))); got != capTokens(big, 8000, false) {
		t.Errorf("small profile, max_tokens=8000: the profile's default won over the caller's value")
	}
}

func TestCapMiddleware_UnderTheCapIsUnchanged(t *testing.T) {
	short := "Found 3 results\n\n| a | b |\n"
	if got := text(capCall(t, &Server{}, `{}`, textOf(short))); got != short {
		t.Errorf("a result under the cap changed:\n%q\nwant\n%q", got, short)
	}
}

func TestCapMiddleware_ErrorsAreNotCut(t *testing.T) {
	result := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: big}}}
	if got := text(capCall(t, &Server{small: true}, `{}`, result)); got != big {
		t.Errorf("an error result was cut to %d characters", len(got))
	}
}

func TestCapMiddleware_AResultTheToolCutIsNotCutTwice(t *testing.T) {
	for _, isJSON := range []bool{false, true} {
		cut := capTokens(big, 500, isJSON)
		got := text(capCall(t, &Server{}, `{"max_tokens":500}`, textOf(cut)))
		if got != cut {
			t.Errorf("isJSON=%v: the server cut output the tool had already cut:\n%s", isJSON, got[len(got)-400:])
		}
		if n := strings.Count(got, "[truncated"); n != 1 {
			t.Errorf("isJSON=%v: %d truncation notices, want 1", isJSON, n)
		}
	}
}

func TestCapMiddleware_ANonIntegerValueIsLeftToTheSchema(t *testing.T) {
	if got := text(capCall(t, &Server{}, `{"max_tokens":"lots"}`, textOf(big))); got != big {
		t.Errorf("a malformed max_tokens was treated as a cap")
	}
}

func TestCapMiddleware_JSONIsCutWithTheJSONNotice(t *testing.T) {
	doc := "[\n" + strings.Repeat("  {\"name\": \"abcdefghij\"},\n", 3000) + "  {}\n]"
	got := text(capCall(t, &Server{}, `{}`, textOf(doc)))
	if !strings.Contains(got, "no longer valid JSON") {
		t.Errorf("a cut JSON result does not say it is no longer JSON:\n%s", got[len(got)-300:])
	}
}

func TestSmallMiddleware_NoSnapshotNamesTheCommand(t *testing.T) {
	eng, err := engine.New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{small: true, eng: eng, cfg: config.Default()}
	called := false
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called = true
		return textOf("tool output"), nil
	}
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: "explore", Arguments: json.RawMessage(`{"focus":"x"}`)}}
	out, err := s.smallMiddleware(handler)(context.Background(), "tools/call", req)
	if err != nil {
		t.Fatal(err)
	}
	ctr := out.(*mcp.CallToolResult)
	if called || !ctr.IsError {
		t.Fatalf("with no snapshot the call reached the tool (called=%v, isError=%v)", called, ctr.IsError)
	}
	if want := "`" + commandName() + " --generate`"; !strings.Contains(text(ctr), want) {
		t.Errorf("the message does not name %s:\n%s", want, text(ctr))
	}
	if strings.Contains(text(ctr), "generate_snapshot") {
		t.Errorf("the message names generate_snapshot, which the small profile does not serve:\n%s", text(ctr))
	}
}

func TestWithMaxTokens(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{``, `{"max_tokens":-1}`},
		{`null`, `{"max_tokens":-1}`},
		{`{"focus":"a/b"}`, `{"focus":"a/b","max_tokens":-1}`},
		{`{"focus":"a","max_tokens":300}`, `{"focus":"a","max_tokens":-1}`},
	} {
		got, ok := withMaxTokens(json.RawMessage(c.in), -1)
		if !ok || string(got) != c.want {
			t.Errorf("withMaxTokens(%s) = %s, %v; want %s", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{`{"max_tokens":"x"}`, `{"max_tokens":1.5}`, `[1]`} {
		if got, ok := withMaxTokens(json.RawMessage(in), -1); ok || string(got) != in {
			t.Errorf("withMaxTokens(%s) = %s, %v; want it left for the schema to refuse", in, got, ok)
		}
	}
}

func TestCompactJSON_IsMarshalsOutput(t *testing.T) {
	value := map[string]any{"b": []any{1, "two", map[string]any{"c": "<&>"}}, "a": nil}
	indented, _ := json.MarshalIndent(value, "", "  ")
	compact, _ := json.Marshal(value)
	if got := compactJSON(string(indented)); got != string(compact) {
		t.Errorf("compactJSON = %s, want json.Marshal's %s", got, compact)
	}
	for _, notJSON := range []string{"Found **3** insight(s).\n", "[1,2]\n\n... (showing 2 of 9)"} {
		if got := compactJSON(notJSON); got != notJSON {
			t.Errorf("compactJSON changed text that is not one JSON document: %q", got)
		}
	}
}

func TestSmallTools_EveryNameIsARegisteredTool(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range defaultToolNames(t) {
		registered[name] = true
	}
	for _, tool := range smallTools {
		if !registered[tool.name] {
			t.Errorf("smallTools names %q, which the default server does not register", tool.name)
		}
		if strings.Contains(tool.line, "\n") {
			t.Errorf("%s: the small description is not one line", tool.name)
		}
	}
}

func mustEngine(t *testing.T) *engine.Engine {
	t.Helper()
	eng, err := engine.New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// defaultToolNames lists the default server's tools over an in-memory session.
func defaultToolNames(t *testing.T) []string {
	t.Helper()
	s, err := New(mustEngine(t), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.mcp.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}
