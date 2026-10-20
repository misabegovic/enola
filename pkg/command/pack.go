package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/enola-labs/enola/pkg/check"
	"github.com/enola-labs/enola/pkg/grade"
	"github.com/enola-labs/enola/pkg/pack"
)

// Pack answers what to open within a budget: the ranges of the tests and symbols
// named inside the paths named, then each named file whole. It is plan's shape,
// read-only in the same way, and it refuses what it cannot vouch for by name.
func (r *Runner) Pack(ctx context.Context, args []string) {
	if code := r.pack(ctx, args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func (r *Runner) pack(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		pathsFlag = fs.String("paths", "", "comma-separated repo-relative files or directories; names are matched only inside them, and each file is also answered whole")
		namesFlag = fs.String("names", "", "comma-separated names or name filters: a test matches when this is part of the name its runner prints, a symbol when this is part of its name")
		budget    = fs.Int("budget", 0, "the budget in estimated tokens (four characters each); required, there is no default")
		asJSON    = fs.Bool("json", false, "emit the answer as JSON instead of text")
	)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: "+r.name()+" pack --budget N [flags] [path...] [repo_path|config_path]\n\n"+
			"What to open within a budget: the line ranges of the tests and symbols named\n"+
			"inside the paths named, in the order asked, then each named file whole. Tests\n"+
			"and symbols are taken while they fit and the rest are counted as left out; a\n"+
			"whole file bigger than what remains is marked to search; a symbol whose end\n"+
			"was never measured is listed at its start line. The counted total never\n"+
			"exceeds the budget, and tokens are an estimate at four characters each.\n\n"+
			"It reads the snapshot on disk and the working tree and writes nothing. A file\n"+
			"whose content moved since the snapshot is named with no range. With no\n"+
			"snapshot, one written under another config, or one another build wrote, it\n"+
			"answers nothing and says why.\n\n"+
			"Positional arguments naming an existing directory or a .yaml/.yml file pick\n"+
			"the repository or config (first match wins); every other positional is a\n"+
			"path. Use --paths for a path that is itself a directory.\n\n"+
			"Exit codes:\n"+
			"  0  the answer was produced\n"+
			"  2  it could not answer (no budget, no path, a snapshot it cannot vouch for)\n\nFlags:\n")
		fs.PrintDefaults()
		r.writeHelpFooter(stderr, args, "pack")
	}
	if err := fs.Parse(args); err != nil {
		return check.StatusUsageError.ExitCode()
	}
	repoArg, paths := splitPlanPositionals(fs.Args())
	paths = append(splitList(*pathsFlag), paths...)
	names := splitList(*namesFlag)
	if *budget <= 0 || len(paths) == 0 {
		fs.Usage()
		return check.StatusUsageError.ExitCode()
	}

	tgt := r.resolveTarget(repoArg)
	fmt.Fprintf(stderr, r.name()+" pack: %s\n", tgt.configNote)
	eng := tgt.engine
	eng.SetPersistCache(false)
	anchor := tgt.repoPaths[0]
	label := filepath.Base(anchor)

	answer, reason := packAnswer(eng, anchor, label, pack.Request{Paths: paths, Names: names, Budget: *budget})
	if reason != "" {
		r.writeFatal(stderr, "pack", "%s", reason)
		return check.StatusUsageError.ExitCode()
	}
	if *asJSON {
		out, err := answer.JSON()
		if err != nil {
			r.writeFatal(stderr, "pack", "encoding the answer: %v", err)
			return check.StatusUsageError.ExitCode()
		}
		fmt.Fprintln(stdout, string(out))
		return 0
	}
	fmt.Fprint(stdout, answer.Render())
	return 0
}

// packAnswer reads the repository's snapshot, refuses it whole for the reasons a
// pin reuse refuses one, and otherwise answers per file.
func packAnswer(eng *bootstrap.Engine, repoPath, label string, req pack.Request) (*pack.Report, string) {
	outDir := eng.OutputDir(repoPath)
	meta, err := grade.ReadSnapshotMeta(outDir)
	if err != nil {
		meta = nil
	}
	if reason := pack.Refusal(label, meta, eng.CurrentMeta(repoPath)); reason != "" {
		return nil, reason
	}
	snap, err := bootstrap.LoadSnapshotDir(outDir)
	if err != nil {
		return nil, label + "'s snapshot could not be read: " + err.Error()
	}
	answer, err := pack.Compute(req, repoFacts(snap.Facts, snap.Meta.Label()), pack.Tree{
		ReadFile: func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(rel))) },
		IsDir: func(rel string) bool {
			info, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(rel)))
			return err == nil && info.IsDir()
		},
		Recorded: pack.Recorded(meta),
	})
	if err != nil {
		return nil, err.Error()
	}
	return answer, ""
}

// repoFacts gives the facts repo-relative files: an append-mode snapshot prefixes
// every file with its repository's label.
func repoFacts(all []facts.Fact, label string) []facts.Fact {
	prefix := label + "/"
	prefixed := prefixedSnapshot(all, prefix)
	out := make([]facts.Fact, 0, len(all))
	for _, f := range all {
		if f.Repo != "" && f.Repo != label {
			continue
		}
		if rest, ok := strings.CutPrefix(f.File, prefix); ok && prefixed {
			f.File = rest
		}
		out = append(out, f)
	}
	return out
}

func prefixedSnapshot(all []facts.Fact, prefix string) bool {
	modules := 0
	for _, f := range all {
		if f.Kind != facts.KindModule || f.File == "" {
			continue
		}
		if !strings.HasPrefix(f.File, prefix) {
			return false
		}
		modules++
	}
	return modules > 0
}
