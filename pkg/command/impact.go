package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Impact answers what the impact_analysis MCP tool answers: what transitively
// depends on a node, grouped by hop, with the pages anchored to its file.
//
// Headless readers could only walk the graph themselves until it existed, and
// one hop of calls read from a facts file is not the tool's answer. This one
// calls the registered tool, with its size controls, so a caller with a short
// window asks for the size it can hold.
func (r *Runner) Impact(ctx context.Context, args []string) {
	os.Exit(r.runImpact(ctx, args, os.Stdout, os.Stderr))
}

func (r *Runner) runImpact(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, r.name()+" impact: "+format+"\n", args...)
	}

	fs := flag.NewFlagSet("impact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		maxDepth       = fs.Int("max-depth", 0, "how many hops of impact to compute, 1-10 (default the tool's, 3)")
		maxNodes       = fs.Int("max-nodes", 0, "maximum impacted nodes to return, 1-500 (default the tool's, 200); the total reported stays exact")
		includeForward = fs.Bool("include-forward", false, "also include what the target depends on")
		outputMode     = fs.String("output-mode", "", "summary (the tool's default), compact, or full")
		maxTokens      = fs.Int("max-tokens", 0, "a hard cap on output size in approximate tokens, cut on a line boundary with the tool's notice")
		asJSON         = fs.Bool("json", false, "the tool's full mode, its raw JSON; the same as --output-mode full")
		asList         = fs.Bool("list", false, "the dependents by depth, with their files; the same as --output-mode compact")
	)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr,
			"Usage: "+r.name()+" impact [flags] <target> [repo_path|config_path]\n\n"+
				"Compute the blast radius of changing a node in the snapshot on disk: everything\n"+
				"that transitively depends on it, grouped by hop depth, and the knowledge pages\n"+
				"anchored to its file. <target> is matched as the impact_analysis MCP tool\n"+
				"matches it, and the output is the tool's own, in the mode asked for. Narrow a\n"+
				"target with a scope prefix, quoted as one argument: 'kind:symbol Alpha',\n"+
				"'file:pkg/a Alpha', 'repo:backend Alpha'. A path to a source file reaches what\n"+
				"depends on anything the file declares.\n\n"+
				"Use endpoint when what you have is a URL; use this when you have a symbol.\n\n"+
				"The snapshot is read, never generated: with none on disk this exits 2 and says\n"+
				"how to generate one.\n\n"+
				"Exit codes:\n"+
				"  0  the report was produced\n"+
				"  2  the command could not run, the tool reported an error, or nothing was\n"+
				"     traversed because the target matched too many nodes to pick from\n\nFlags:\n")
		fs.PrintDefaults()
		r.writeHelpFooter(fs.Output(), args, "impact")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	// Flag parsing stops at the target, so a later flag or a third word would be misread silently.
	if len(rest) > 2 {
		say("expected <target> [repo_path|config_path], got %d arguments; quote a target that contains a space", len(rest))
		return 2
	}
	for _, arg := range rest {
		if strings.HasPrefix(arg, "-") {
			say("flags go before the target; %q came after it", arg)
			return 2
		}
	}
	// Refused, not clamped: a walk cut short would still report an "exact" total.
	if *maxDepth < 0 || *maxDepth > 10 {
		say("--max-depth %d is outside 1-10", *maxDepth)
		return 2
	}
	if *maxNodes < 0 || *maxNodes > 500 {
		say("--max-nodes %d is outside 1-500", *maxNodes)
		return 2
	}
	if *asJSON && *asList {
		say("--json and --list are two renderings of one report; pass one")
		return 2
	}
	mode := *outputMode
	if *asJSON {
		if mode != "" && mode != "full" {
			say("--json is the full mode and cannot be combined with --output-mode %s", mode)
			return 2
		}
		mode = "full"
	}
	if *asList {
		if mode != "" && mode != "compact" {
			say("--list is the compact mode and cannot be combined with --output-mode %s", mode)
			return 2
		}
		mode = "compact"
	}

	call := map[string]any{"target": rest[0]}
	if *maxDepth != 0 {
		call["max_depth"] = *maxDepth
	}
	if *maxNodes != 0 {
		call["max_nodes"] = *maxNodes
	}
	if *includeForward {
		call["include_forward"] = true
	}
	if mode != "" {
		call["output_mode"] = mode
	}
	if *maxTokens != 0 {
		call["max_tokens"] = *maxTokens
	}
	target := ""
	if len(rest) == 2 {
		target = rest[1]
	}

	tgt := r.resolveTarget(target)
	eng := tgt.engine
	cfg := eng.Config()
	dir := eng.OutputDir(tgt.repoPaths[0])
	if _, err := os.Stat(filepath.Join(dir, "facts.jsonl")); err != nil {
		named := target
		if named == "" {
			named = "."
		}
		say("no snapshot at %s; run `%s --generate %s` first", dir, r.name(), named)
		return 2
	}
	if !restoreSnapshot(eng, cfg) {
		say("the snapshot at %s could not be restored", dir)
		return 2
	}
	res, err := r.callToolResult(ctx, eng, cfg, "impact_analysis", call)
	if err != nil {
		say("%v", err)
		return 2
	}
	_, _ = io.WriteString(stdout, withFinalNewline(resultText(res)))
	if res.IsError {
		return 2
	}

	// An empty list that exits 0 would read as "nothing depends on this".
	resolution, _ := resultField(res, "resolution").(map[string]any)
	if resolution == nil {
		return 0
	}
	if matched, _ := resolution["matched"].(string); matched == "" {
		say("%q matches too many nodes to pick one, so nothing was traversed; "+
			"pass an exact name or scope it with repo:, kind: or file:", rest[0])
		return 2
	}
	if ambiguous, _ := resolution["ambiguous"].(bool); ambiguous {
		say("%q matched several nodes; this report is about %s. "+
			"Pass that name, or a scope, to ask without the guess", rest[0], resolution["matched"])
	}
	return 0
}
