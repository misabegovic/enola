package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/pkg/bootstrap"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// askRestoredTool answers one MCP tool call at the command line and exits: it
// restores the snapshot the target names, builds the MCP server over it, and
// calls the registered tool through an in-memory transport, printing the tool's
// text as it is. The answer is therefore the tool's answer, down to its
// ambiguity notes and its truncation notice, and not a second reading of the
// graph that could drift from it.
//
// It never generates a snapshot. A question about structure is answered from the
// snapshot a session, a baseline and every other reader already hold, so with
// none on disk it exits 2 naming --generate and writes nothing.
func (r *Runner) askRestoredTool(ctx context.Context, command, target, tool string, args map[string]any) {
	tgt := r.resolveTarget(target)
	eng := tgt.engine
	cfg := eng.Config()
	dir := eng.OutputDir(tgt.repoPaths[0])
	if _, err := os.Stat(filepath.Join(dir, "facts.jsonl")); err != nil {
		named := target
		if named == "" {
			named = "."
		}
		fmt.Fprintf(os.Stderr, "%s %s: no snapshot at %s; run `%s --generate %s` first\n", r.name(), command, dir, r.name(), named)
		os.Exit(2)
	}
	if !restoreSnapshot(eng, cfg) {
		fmt.Fprintf(os.Stderr, "%s %s: the snapshot at %s could not be restored\n", r.name(), command, dir)
		os.Exit(2)
	}
	text, isError, err := r.callTool(ctx, eng, cfg, tool, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", r.name(), command, err)
		os.Exit(2)
	}
	// Stdout carries the tool's text whether or not the tool reported an error,
	// so what a reader parses is always the tool's answer; the exit code is what
	// says which kind it was. Stderr is left to the engine's own log lines.
	fmt.Fprint(os.Stdout, withFinalNewline(text))
	if isError {
		os.Exit(2)
	}
}

// callTool connects a client to a server over the restored engine in process
// and makes one call, returning the concatenated text content and whether the
// tool reported an error.
func (r *Runner) callTool(ctx context.Context, eng *bootstrap.Engine, cfg *config.Config, tool string, args map[string]any) (string, bool, error) {
	res, err := r.callToolResult(ctx, eng, cfg, tool, args)
	if err != nil {
		return "", false, err
	}
	return resultText(res), res.IsError, nil
}

// callToolResult is callTool returning the whole result: its text, and the
// structured content and metadata beside it.
func (r *Runner) callToolResult(ctx context.Context, eng *bootstrap.Engine, cfg *config.Config, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	srv, err := bootstrap.NewServerOverRestored(eng, cfg, bootstrap.Binary(r.name(), r.buildVersion()))
	if err != nil {
		return nil, err
	}
	serverT, clientT := newInProcessTransports()
	if _, err := srv.MCP().Connect(ctx, serverT, nil); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: r.name(), Version: r.buildVersion()}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// resultText is a tool result's text content, its blocks joined.
func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// resultField reads one top-level field of a result's structured content.
func resultField(res *mcp.CallToolResult, field string) any {
	if m, ok := res.StructuredContent.(map[string]any); ok {
		return m[field]
	}
	return nil
}

// resultMeta reads one string a tool left in its result's metadata.
func resultMeta(res *mcp.CallToolResult, key string) string {
	if v, ok := res.Meta[key].(string); ok {
		return v
	}
	return ""
}

// withFinalNewline ends a terminal answer on a newline without touching the
// tool's text otherwise: JSON results carry none, and a prompt printed on the
// last line of an answer reads as part of it.
func withFinalNewline(text string) string {
	if strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// stringList is a flag that collects every value it is given, in order.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// commaList is a repeatable flag whose values may also be comma-separated, for
// the closed vocabularies (relation kinds) a caller writes several of at once.
type commaList []string

func (l *commaList) String() string { return strings.Join(*l, ",") }

func (l *commaList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}
