package command

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/enola-labs/enola/pkg/check"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConstraintsLedger is `enola constraints ledger [--json [--breaches]] [--repo label]
// [repo_path|config]`: how much of the declared law is being excused rather than
// obeyed.
//
// The gate has always collected the inputs — a suppression entry and an
// `exempt:` carve-out each name an owner, a reason and a date — and never
// asked the question they answer. A per-run verdict reports each excuse in
// isolation, which is exactly the view in which a rule that everybody signs
// away looks like thirty separate reasonable decisions.
//
// Read from the SNAPSHOT's own intent facts rather than from the working
// tree's YAML, deliberately: the rules counted here are the rules that
// produced the findings being counted beside them, so the two halves of every
// ratio come from one state of the law. A rule edited since the snapshot is
// therefore absent, and the report prints the snapshot's age so a reader can
// tell.
//
// A report, never a gate: exit 0 whenever a report was produced, 2 when it
// could not run. Nothing here changes what `enola check` fails on — a gate
// that failed on its own unpopularity is one nobody would leave enabled.
func (r *Runner) ConstraintsLedger(args []string) {
	fs := flag.NewFlagSet("constraints ledger", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print the ledger as JSON")
	breaches := fs.Bool("breaches", false, "with --json, list under each rule every breach it counted, with the finding identity check pairs on")
	member := fs.String("repo", "", "the repository whose law to report, by its label, when the snapshot holds several")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: "+r.name()+" constraints ledger [--json [--breaches]] [--repo label] [repo_path|config]\n\n"+
			"Every declared rule, its enforcement mode, the breaches it raised in the\n"+
			"current snapshot, and how many of those a suppression or an exemption\n"+
			"excused — with each excuse's owner, reason and age.\n\n"+
			"The rate is evidence for a human: a rule whose breaches are mostly signed\n"+
			"away is a rule to reconsider, and that is invisible one verdict at a time.\n"+
			"A report, never a gate — nothing here fails a build.\n\n"+
			"--breaches adds each rule's breach_list: identity, title, evidence file and\n"+
			"symbol, and whether a suppression matched, one entry per counted breach.\n\n"+
			"Named a config listing several repositories, the ledger reads their union,\n"+
			"and --repo names the member whose suppressions and drift it reports.\n")
		fs.PrintDefaults()
		r.writeHelpFooter(fs.Output(), args, "constraints ledger")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(check.StatusUsageError.ExitCode())
	}
	// The list is for a reader that parses; printed as text it would bury the
	// excuse rate the report exists to show under every breach it counted.
	if *breaches && !*asJSON {
		fmt.Fprintln(os.Stderr, "constraints ledger: --breaches lists breaches in the JSON form; pass --json with it")
		os.Exit(check.StatusUsageError.ExitCode())
	}
	repoPath := "."
	if fs.NArg() > 0 {
		repoPath = fs.Arg(0)
	}
	repoPath, _ = filepath.Abs(repoPath)

	tgt := r.resolveTarget(repoPath)
	tgt.engine.SetPersistCache(false)
	res, err := r.ledgerAnswer(context.Background(), tgt, repoPath, *member, *breaches, *asJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "constraints ledger: %v\n", err)
		os.Exit(check.StatusUsageError.ExitCode())
	}
	if res.IsError {
		fmt.Fprintf(os.Stderr, "constraints ledger: %s\n", resultText(res))
		os.Exit(check.StatusUsageError.ExitCode())
	}
	fmt.Print(resultText(res))
	os.Exit(0)
}

// LedgerDocument is `constraints ledger --json`; the constraints_ledger tool
// answers it.
type LedgerDocument = check.LedgerDocument

// ledgerAnswer restores the target's snapshot and asks the constraints_ledger
// tool over it, for the text or, with asJSON, the document. The snapshot is
// read once, by the restore; the tool computes how far the tree has moved from
// the meta that read carried.
func (r *Runner) ledgerAnswer(ctx context.Context, tgt target, repoPath, member string, breaches, asJSON bool) (*mcp.CallToolResult, error) {
	eng := tgt.engine
	cfg := eng.Config()
	// A config names no output directory of its own: the snapshot is the first
	// member's, as a cluster writes its union to every member and lint reads it.
	if _, err := os.Stat(filepath.Join(eng.OutputDir(tgt.repoPaths[0]), "facts.jsonl")); err != nil || !restoreSnapshot(eng, cfg) {
		return nil, fmt.Errorf("no snapshot for %s; generate one first", repoPath)
	}
	format := "text"
	if asJSON {
		format = "json"
	}
	return r.callToolResult(ctx, eng, cfg, "constraints_ledger", map[string]any{"repo": member, "breaches": breaches, "format": format, "max_tokens": -1})
}

// snapshotStaleness is how far repoPath has moved since the snapshot whose meta
// this is, in one line, or empty when nothing has. The meta the snapshot was
// read with carries every file hash the comparison needs, so asking needs no
// second read of the snapshot. An unanswerable comparison is said, never passed
// off as clean.
func snapshotStaleness(eng *bootstrap.Engine, repoPath string, meta facts.SnapshotMeta) string {
	if d, err := eng.DriftFromMeta(repoPath, meta); err == nil && (d.Unknown || d.Any()) {
		return d.Summary(5)
	}
	return ""
}
