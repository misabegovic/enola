package command

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// Path answers what the find_path MCP tool answers: how one node reaches
// another, or, with --from-node and --to-node, how any of one set of nodes
// reaches any of another in one search.
//
// Until it existed that question could only be asked inside a session, so a
// script, a hook or a tool built on the engine re-derived the walk itself, and a
// second walk is a second answer. This one calls the registered tool.
func (r *Runner) Path(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("path", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var fromNodes, toNodes stringList
	var relationKinds commaList
	fs.Var(&fromNodes, "from-node", "an exact node name to start from, repeatable; used with --to-node instead of <from> <to>")
	fs.Var(&toNodes, "to-node", "an exact node name to reach, repeatable; used with --from-node instead of <from> <to>")
	fs.Var(&relationKinds, "relation-kinds", "relation kinds to follow, repeatable or comma-separated (default all)")
	maxDepth := fs.Int("max-depth", 0, "maximum path length to search, 1-20 (default the tool's, 10)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr,
			"Usage: "+r.name()+" path [flags] <from> <to> [repo_path|config_path]\n"+
				"       "+r.name()+" path [flags] --from-node <name>... --to-node <name>... [repo_path|config_path]\n\n"+
				"Find the shortest path between two nodes in the snapshot on disk: how does A\n"+
				"reach B? <from> and <to> are matched as the find_path MCP tool matches them,\n"+
				"with its disambiguation and the candidates it tried. --from-node and\n"+
				"--to-node take exact node names instead, and one search returns the shortest\n"+
				"path from any of the first to any of the second, listing names the graph does\n"+
				"not hold.\n\n"+
				"The output is the tool's own JSON; an error the tool reports is printed as\n"+
				"it words it and exits 2. The snapshot is read, never generated: with none on\n"+
				"disk this exits 2 and says how to generate one.\n\n"+
				"Flags:\n")
		fs.PrintDefaults()
		r.writeHelpFooter(os.Stderr, args, "path")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	rest := fs.Args()
	call := map[string]any{}
	target := ""
	switch {
	case len(fromNodes) > 0 || len(toNodes) > 0:
		if len(rest) > 1 {
			fmt.Fprintf(os.Stderr, "%s path: with --from-node and --to-node the only argument is the repository or config; got %d\n", r.name(), len(rest))
			os.Exit(2)
		}
		if len(rest) == 1 {
			target = rest[0]
		}
		if len(fromNodes) > 0 {
			call["from_nodes"] = []string(fromNodes)
		}
		if len(toNodes) > 0 {
			call["to_nodes"] = []string(toNodes)
		}
	case len(rest) >= 2 && len(rest) <= 3:
		call["from"], call["to"] = rest[0], rest[1]
		if len(rest) == 3 {
			target = rest[2]
		}
	default:
		fs.Usage()
		os.Exit(2)
	}
	if len(relationKinds) > 0 {
		call["relation_kinds"] = []string(relationKinds)
	}
	if *maxDepth != 0 {
		call["max_depth"] = *maxDepth
	}
	r.askRestoredTool(ctx, "path", target, "find_path", call)
}
