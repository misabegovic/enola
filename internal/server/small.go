package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Option configures a Server when it is built.
type Option func(*Server)

// SmallProfile serves the profile for a client with a small context window: the
// tools in smallTools, each described in one line, short instructions, results
// as compact JSON, and a default cap of smallMaxTokens.
func SmallProfile() Option { return func(s *Server) { s.small = true } }

// The default caps, each a quarter of the window its profile is for: 32,000
// tokens for the default server and 8,192 for the small profile. A stated rule,
// not a measured optimum. A caller's positive max_tokens overrides the default
// and a negative one removes the cap.
const (
	defaultMaxTokens = 8000
	smallMaxTokens   = 2000
)

// maxTokensDescription is the max_tokens text a profile's tools carry. The
// struct tags spell out the default server's, since a tag cannot name a
// constant; the small profile rewrites it in the schema it serves, shorter
// because its instructions already say how the cap works.
func maxTokensDescription(small bool) string {
	if small {
		return fmt.Sprintf("Approximate token cap on the result. Default: %d; a negative value means no cap.", smallMaxTokens)
	}
	return fmt.Sprintf("Cap on output size in approximate tokens (four characters each); "+
		"output over it is cut on a line boundary with a notice. Default: %d. A negative value means no cap.", defaultMaxTokens)
}

// smallTools is the whole --small profile. A tool not named here is not
// registered under it, so a tool added upstream stays out until someone decides
// it belongs. generate_snapshot, set_baseline and diff_snapshot are left out on
// purpose: the session hooks and the CLI pin and grade, and a small model asked
// to snapshot a large repository spends its turn waiting.
var smallTools = []struct{ name, line string }{
	{"explore", "What a module, file, symbol or directory holds, what it depends on and what depends on it."},
	{"impact_analysis", "What transitively depends on a target, so you know what a change to it can break."},
	{"governing_intent", "The knowledge pages (decisions, specs) that govern a file or fact, or the code a page governs."},
	{"constraints_for", "The declared rules that bind a file or fact, and the current violations naming it. Ask before editing."},
	{"query_insights", "Findings computed at snapshot time: dependency cycles, layer violations, hotspots, dead code and more."},
	{"context_pack", "Which line ranges of the tests and symbols named inside the given paths to open, within a token budget."},
}

func smallLine(name string) (string, bool) {
	for _, t := range smallTools {
		if t.name == name {
			return t.line, true
		}
	}
	return "", false
}

func (s *Server) serves(name string) bool {
	if !s.small {
		return true
	}
	_, ok := smallLine(name)
	return ok
}

// addTool infers the schema itself under --small, rather than leaving that to
// mcp.AddTool, because a struct tag cannot say which profile it is served under
// and the small profile's max_tokens default differs from the tag's.
func addTool[In, Out any](s *Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if s.small {
		line, ok := smallLine(t.Name)
		if !ok {
			return
		}
		schema, err := jsonschema.For[In](nil)
		if err != nil {
			panic(fmt.Sprintf("addTool: tool %q: %v", t.Name, err))
		}
		if p := schema.Properties["max_tokens"]; p != nil {
			p.Description = maxTokensDescription(true)
		}
		short := *t
		short.Description = line
		short.InputSchema = schema
		t = &short
	}
	mcp.AddTool(s.mcp, t, h)
}

// commandName is the name this binary was started under, so a remedy names the
// command the user actually has on their PATH.
func commandName() string {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "enola"
	}
	return name
}

func smallInstructions() string {
	bin := commandName()
	return "enola serves this repository's architecture as facts: modules, symbols, routes, storage and their dependencies. " +
		"Ask it before reading files to see how code is wired: explore for what a module, file or symbol holds and touches, " +
		"impact_analysis for what depends on a target, governing_intent for the pages that govern a file, " +
		"constraints_for for the rules that bind a file, query_insights for findings such as cycles and layer violations, " +
		"and context_pack for which line ranges to open within a budget. " +
		fmt.Sprintf("Baselines are pinned and changes graded by the session hooks or on the command line (`%s baseline pin`, `%s check`), not by these tools. ", bin, bin) +
		fmt.Sprintf("Results are capped at about %d tokens: pass max_tokens for more, or a negative max_tokens for all of it.", smallMaxTokens)
}

// refreshHint is the remedy the freshness banner names. The small profile does
// not serve generate_snapshot, so there it names the command line instead.
func (s *Server) refreshHint() string {
	if s.small {
		return fmt.Sprintf("Run `%s --generate` in the repository to refresh.", commandName())
	}
	return "Run generate_snapshot to refresh."
}

// capMiddleware cuts every successful tool result at the caller's max_tokens, or
// at the profile's default when the caller named none. Tools that cap
// themselves have already cut to an explicit value, and CapTokens leaves such
// output as it is. An error is never cut: it is short, and it is the part of a
// failed call the caller has to read whole.
func (s *Server) capMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
		if !ok {
			return next(ctx, method, req)
		}
		asked, ok := maxTokensArg(params.Arguments)
		if !ok {
			// Not an integer, so the tool's schema refuses the call with an error
			// that says why. Nothing here to cut.
			return next(ctx, method, req)
		}
		limit := defaultMaxTokens
		if s.small {
			limit = smallMaxTokens
		}
		if asked != 0 {
			limit = asked
		}
		result, err := next(ctx, method, req)
		if err != nil || limit < 0 {
			return result, err
		}
		if ctr, ok := result.(*mcp.CallToolResult); ok && ctr != nil && !ctr.IsError {
			capResult(ctr, limit)
		}
		return result, err
	}
}

// smallMiddleware is the small profile's handling of a call to a tool it
// serves. With no snapshot it names the command that makes one, since the tool
// that would is not served. Otherwise it asks the tool for its whole result and
// compacts JSON before capMiddleware cuts it, because a cut result is no longer
// JSON that could be compacted.
func (s *Server) smallMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
		if !ok || !s.serves(params.Name) {
			return next(ctx, method, req)
		}
		if store := s.eng.Store(); store == nil || store.Count() == 0 {
			return errorResult(fmt.Sprintf("No snapshot is loaded for %s. Run `%s --generate` in that repository to make one, then call this tool again.",
				s.activeRepo(), commandName())), nil
		}
		if whole, ok := withMaxTokens(params.Arguments, -1); ok {
			params.Arguments = whole
		}
		result, err := next(ctx, method, req)
		if ctr, ok := result.(*mcp.CallToolResult); ok && ctr != nil && err == nil {
			for _, c := range ctr.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					tc.Text = compactJSON(tc.Text)
				}
			}
		}
		return result, err
	}
}

func maxTokensArg(raw json.RawMessage) (int, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, true
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return 0, false
	}
	v, present := args["max_tokens"]
	if !present || string(bytes.TrimSpace(v)) == "null" {
		return 0, true
	}
	var n float64
	if err := json.Unmarshal(v, &n); err != nil || n != math.Trunc(n) {
		return 0, false
	}
	return int(math.Max(math.Min(n, math.MaxInt32), -1)), true
}

// withMaxTokens leaves arguments that are not an object, or whose max_tokens is
// not an integer, as they are: the tool's schema must still be the one to refuse
// them, with its own error.
func withMaxTokens(raw json.RawMessage, n int) (json.RawMessage, bool) {
	if _, ok := maxTokensArg(raw); !ok {
		return raw, false
	}
	args := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return raw, false
		}
	}
	args["max_tokens"] = json.RawMessage(fmt.Sprint(n))
	out, err := json.Marshal(args)
	if err != nil {
		return raw, false
	}
	return out, true
}

// compactJSON re-encodes text that is one whole JSON document without
// insignificant whitespace, which is json.Marshal's output for what
// json.MarshalIndent wrote. Anything else is returned as it is.
func compactJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if !looksJSON(trimmed) || !json.Valid([]byte(trimmed)) {
		return text
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err != nil {
		return text
	}
	return buf.String()
}

func looksJSON(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// capResult cuts a result's text at limit tokens. A result is one text block in
// every tool here; one made of several is joined before it is cut, so the limit
// is on what the caller reads rather than on each block.
func capResult(ctr *mcp.CallToolResult, limit int) {
	var blocks []*mcp.TextContent
	for _, c := range ctr.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			return
		}
		blocks = append(blocks, tc)
	}
	if len(blocks) == 1 {
		blocks[0].Text = capTokens(blocks[0].Text, limit, looksJSON(blocks[0].Text))
		return
	}
	var joined strings.Builder
	for _, tc := range blocks {
		joined.WriteString(tc.Text)
	}
	if capped := capTokens(joined.String(), limit, looksJSON(joined.String())); capped != joined.String() {
		ctr.Content = []mcp.Content{&mcp.TextContent{Text: capped}}
	}
}
