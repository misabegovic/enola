package command

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/enola-labs/enola/pkg/check"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Constraints is `enola constraints <lint|mine|init|explain|ledger>` — the
// authoring loop for the declared-constraint vocabulary. `mine` (see
// ConstraintsMine) proposes candidate declarations out of the snapshot's own
// regularities; `ledger` (see ConstraintsLedger) reports how much of the law
// already declared is being excused rather than obeyed; `lint`
// parses each repo's declaration
// (enola-intent.yaml, any enola/constraints/*.yaml files, and any
// cluster-config override), reports every
// validation problem with its file context, and — when a snapshot exists on
// disk — resolves each declared component against it so an author sees what a
// selector actually selects BEFORE a rule built on it verdicts anything. It
// never generates a snapshot and never writes: a missing snapshot degrades to
// validation-only, named as such.
//
// Exit codes follow the gate's contract where it overlaps: 0 when every
// declaration is valid, 1 when any validation problem was reported, 2 when the
// command could not run at all.
func (r *Runner) Constraints(args []string) {
	if len(args) > 0 && args[0] == "mine" {
		r.ConstraintsMine(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "init" {
		r.ConstraintsInit(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "explain" {
		r.ConstraintsExplain(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "ledger" {
		r.ConstraintsLedger(args[1:])
		return
	}
	fs := flag.NewFlagSet("constraints", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "lint: print one JSON document instead of the text")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: "+r.name()+" constraints <lint|mine|init|explain|ledger> [repo_path|config_path]\n\n"+
			"lint validates the declared constraint vocabulary — inline in enola-intent.yaml\n"+
			"and per-domain files under enola/constraints/ — and resolves each component\n"+
			"against the current snapshot, if one exists (else validation only).\n"+
			"lint --json prints the same as one JSON document: validity, each problem and\n"+
			"each unknown key with its file and line, and every component with its state.\n\n"+
			"mine searches the current snapshot's fact store for near-invariants and\n"+
			"reports them as candidate constraint declarations — proposals for operator\n"+
			"review, never self-adopting law. Run `"+r.name()+" constraints mine --help`.\n"+
			"init writes a first declaration binding every shipped recipe whose roles\n"+
			"resolve to directories the repository has. Run `"+r.name()+" constraints init --help`.\n"+
			"explain names the components a file's facts belong to, the selector that\n"+
			"admitted each, and the edges the file makes. Run `"+r.name()+" constraints explain --help`.\n"+
			"ledger reports how much of the declared law is being EXCUSED rather than\n"+
			"obeyed — every rule's breaches beside the suppressions and exemptions that\n"+
			"signed them away, with each excuse's owner and age. Run `"+r.name()+" constraints ledger --help`.\n\n"+
			"Exit codes (lint):\n"+
			"  0  every declaration is valid\n"+
			"  1  validation problems were reported\n"+
			"  2  the command could not run\n\n"+
			"Exit codes (mine, init, explain, ledger):\n"+
			"  0  a report was produced\n"+
			"  2  the command could not run — no snapshot to read, or a declaration\n"+
			"     init would have had to overwrite\n")
		r.writeHelpFooter(os.Stderr, args, "constraints")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(check.StatusUsageError.ExitCode())
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] != "lint" || HelpRequested(rest) {
		fs.Usage()
		os.Exit(check.StatusUsageError.ExitCode())
	}
	// The flag set stops at the subcommand's name, so what follows it is
	// parsed again: `constraints lint --json .` reads the way it is typed.
	if err := fs.Parse(rest[1:]); err != nil {
		os.Exit(check.StatusUsageError.ExitCode())
	}
	rest = append([]string{"lint"}, fs.Args()...)

	var arg string
	if len(rest) > 1 {
		arg = rest[1]
	}
	tgt := r.resolveTarget(arg)
	fmt.Fprintf(os.Stderr, r.name()+" constraints lint: %s\n", tgt.configNote)
	// Read-only like check and coverage: linting a declaration must not touch
	// the cache, the snapshot, or anything else on disk.
	tgt.engine.SetPersistCache(false)

	res, err := r.lintAnswer(context.Background(), tgt, *asJSON)
	if err != nil {
		r.constraintsFatal("%v", err)
	}
	if res.IsError {
		r.constraintsFatal("%s", resultText(res))
	}
	fmt.Print(resultText(res))
	if valid, _ := resultField(res, "valid").(bool); !valid {
		os.Exit(1)
	}
	os.Exit(0)
}

// lintAnswer asks the constraints_lint tool for the target's declaration,
// over the snapshot on disk when there is one and over none otherwise, which
// the tool answers as validation only. The first repository's snapshot is the
// one resolved against, as a cluster writes its union to every member.
func (r *Runner) lintAnswer(ctx context.Context, tgt target, asJSON bool) (*mcp.CallToolResult, error) {
	eng := tgt.engine
	cfg := eng.Config()
	if _, err := os.Stat(filepath.Join(eng.OutputDir(tgt.repoPaths[0]), "facts.jsonl")); err == nil {
		restoreSnapshot(eng, cfg)
	}
	format := "text"
	if asJSON {
		format = "json"
	}
	return r.callToolResult(ctx, eng, cfg, "constraints_lint", map[string]any{"format": format, "max_tokens": -1})
}

func (r *Runner) constraintsFatal(format string, args ...any) {
	r.cmdFatal("constraints", format, args...)
}
