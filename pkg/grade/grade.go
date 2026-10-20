// Package grade answers check's question, what a change did against a pinned
// baseline, for every caller that asks it: `enola check`, and the MCP server's
// grade tool. One implementation is the point; a gate and a tool that graded
// the same tree differently would each be a reason to distrust the other.
//
// It has two halves. The currency gate decides whether the snapshot on disk
// is the one a regenerate would produce, so a grade can read it instead of
// re-extracting. Compute grades whatever current side an engine publishes
// against the baseline.
package grade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/conformance"
	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/check"
	pkghistory "github.com/enola-labs/enola/pkg/history"
	"github.com/enola-labs/enola/pkg/plugin"
)

// Gate is what the currency gate reads: where a repository's snapshot lives,
// what this build and config would record for its tree now, and how far the
// tree has moved from a recorded meta.
type Gate interface {
	OutputDir(repoPath string) string
	CurrentMeta(repoPath string) *facts.SnapshotMeta
	DriftFromMeta(repoPath string, meta facts.SnapshotMeta) (engine.Drift, error)
}

// Engine is an engine a grade publishes the current side into and reads it
// back from: the one a command builds for itself, or a scratch one beside a
// server that must not move what it serves.
type Engine interface {
	Gate
	Store() *facts.Store
	Snapshot() *facts.Snapshot
	MetaFor(repoPath string) facts.SnapshotMeta
	Extractors() []plugin.Extractor
	RestoreFromDir(dir string, repoPaths map[string]string, singleRepoLabel string) error
	RestoreMembers(members map[string]facts.SnapshotMeta)
	GenerateSnapshot(ctx context.Context, repoPath string, appendMode bool) (*facts.Snapshot, error)
	SetPersistCache(persist bool)
	SetDeferLinking(deferred bool)
}

// Member is one member's snapshot meta as its dir records it, beside the
// identity this build and config give its tree now.
type Member struct {
	Path     string
	Recorded facts.SnapshotMeta
	Present  facts.SnapshotMeta
}

// Members is the currency gate: every member's recorded and present meta when
// each member's on-disk snapshot describes its working tree under this build
// and config, the reason otherwise. It reads each member's own
// snapshot.meta.json, never the facts: the decision is a few hashes per member,
// not a reload of the union. Any doubt, including a member with no snapshot or
// a meta without file hashes, answers no. A cluster writes one union to every
// member, so the members must also agree on the snapshot id; a fan-out
// interrupted halfway leaves them disagreeing, and that is regenerated rather
// than trusted. The provider set is not compared here: CurrentMeta describes a
// run that has not happened, so it cannot say which providers will run, and an
// equal config hash already proves the same providers are configured.
func Members(eng Gate, repoPaths []string) ([]Member, string) {
	members := make([]Member, 0, len(repoPaths))
	union := ""
	for _, repoPath := range repoPaths {
		name := filepath.Base(repoPath)
		meta, err := ReadSnapshotMeta(eng.OutputDir(repoPath))
		if err != nil {
			return nil, name + " holds no snapshot"
		}
		current := eng.CurrentMeta(repoPath)
		if current == nil {
			return nil, name + " could not be described"
		}
		if meta.ConfigHash != current.ConfigHash {
			return nil, name + "'s snapshot was written under another config"
		}
		for _, kind := range check.BlockingKinds(diff.CompareMeta(*meta, *current)) {
			if kind == diff.WarnProviderSet {
				continue
			}
			return nil, name + "'s snapshot is not comparable with this build: " + string(kind)
		}
		drift, err := eng.DriftFromMeta(repoPath, *meta)
		if err != nil {
			return nil, name + ": " + err.Error()
		}
		if drift.Unknown {
			return nil, name + "'s snapshot carries no file hashes"
		}
		if drift.Count() > 0 {
			return nil, name + " moved since its snapshot: " + drift.Summary(3)
		}
		if union == "" {
			union = meta.SnapshotID
		} else if meta.SnapshotID != union {
			return nil, name + " holds a different union than the first member (a fan-out that did not finish)"
		}
		members = append(members, Member{Path: repoPath, Recorded: *meta, Present: *current})
	}
	return members, ""
}

// ServesCheck asks whether a grade can read the snapshot on disk rather than
// regenerate it, and returns the members to read when it can.
//
// The currency gate comes first and is asked unchanged: a snapshot that is not
// current is not the snapshot a regenerate would produce. A pin freezes that
// snapshot as it stands, but a grade reads two things from it that a pin never
// does, and a reused grade has to be the regenerated one. The git state names
// the files the change touched (ChangedFiles) and the repository the diff
// compares as, and a commit moves HEAD without moving a file. The generation
// time is compared with the baseline's, to say a baseline is stale or a pair
// inverted, and a regenerate would be stamped now. Where either would read
// differently, the verdict or its exit code would move with the reuse, so the
// answer is no and the grade regenerates. Each only declines: nothing here
// trusts a snapshot the currency gate would not.
func ServesCheck(eng Gate, repoPaths []string, baseDir string, now time.Time) ([]Member, string) {
	members, stale := Members(eng, repoPaths)
	if stale != "" {
		return nil, stale
	}
	for _, m := range members {
		name := filepath.Base(m.Path)
		if m.Recorded.RepoPath != m.Present.RepoPath {
			return nil, name + "'s snapshot was written at " + m.Recorded.RepoPath
		}
		if !sameGit(m.Recorded.Git, m.Present.Git) {
			return nil, name + "'s git state moved since its snapshot"
		}
	}
	base, err := ReadSnapshotMeta(baseDir)
	if err != nil {
		return nil, "no baseline meta to compare the snapshot's time with"
	}
	written := members[0].Recorded
	regenerated := written
	regenerated.GeneratedAt = now.UTC().Format(time.RFC3339)
	if !reflect.DeepEqual(diff.CompareMeta(*base, written), diff.CompareMeta(*base, regenerated)) {
		return nil, "the baseline compares differently with the snapshot's time than with now"
	}
	return members, ""
}

func sameGit(a, b *facts.GitInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Restore publishes the snapshot on disk as eng's current one: the union from
// the first member's dir, which a cluster writes whole to every member,
// labelled as the generate that wrote it labelled it, and each member's own
// meta beside it.
func Restore(eng Engine, members []Member) error {
	labels := make(map[string]string, len(members))
	metas := make(map[string]facts.SnapshotMeta, len(members))
	for _, m := range members {
		abs, err := filepath.Abs(m.Path)
		if err != nil {
			return err
		}
		labels[m.Recorded.Label()] = abs
		metas[abs] = m.Recorded
	}
	single := ""
	if len(members) == 1 {
		single = members[0].Recorded.Label()
	}
	if err := eng.RestoreFromDir(eng.OutputDir(members[0].Path), labels, single); err != nil {
		return err
	}
	eng.RestoreMembers(metas)
	return nil
}

// Regenerate publishes a fresh snapshot of every member into eng, in order, the
// later ones appended, and writes nothing: the extractor cache is read but not
// persisted. Linking and the explainers are deferred to the last member's turn,
// as --generate defers them: an earlier turn's links and findings were rebuilt
// over the growing union and discarded, 27 times on a 27-member cluster.
func Regenerate(ctx context.Context, eng Engine, repoPaths []string) error {
	eng.SetPersistCache(false)
	defer eng.SetDeferLinking(false)
	for i, repoPath := range repoPaths {
		eng.SetDeferLinking(i < len(repoPaths)-1)
		if _, err := eng.GenerateSnapshot(ctx, repoPath, i > 0); err != nil {
			return fmt.Errorf("snapshot generation failed for %s: %v", repoPath, err)
		}
	}
	return nil
}

// Publish puts the current side of a grade into eng, given the currency
// gate's answer: the snapshot on disk when the gate found it current, a
// regenerate otherwise, or when the snapshot on disk could not be read after
// all. It reports whether the snapshot on disk was the one published. Nothing
// is written either way.
func Publish(ctx context.Context, eng Engine, repoPaths []string, members []Member, stale string) (bool, error) {
	eng.SetPersistCache(false)
	if stale == "" && len(members) > 0 {
		if err := Restore(eng, members); err == nil {
			return true, nil
		}
	}
	return false, Regenerate(ctx, eng, repoPaths)
}

// ReuseNote is the one line that says a grade read the snapshot on disk rather
// than regenerating it, naming when it was written and where.
func ReuseNote(eng Gate, members []Member) string {
	return fmt.Sprintf("the snapshot written %s in %s matches every working tree under this build and config; graded without regenerating",
		members[0].Recorded.GeneratedAt, eng.OutputDir(members[0].Path))
}

// ReadSnapshotMeta reads the snapshot.meta.json of a directory that holds a
// snapshot.
func ReadSnapshotMeta(dir string) (*facts.SnapshotMeta, error) {
	if _, err := os.Stat(filepath.Join(dir, "facts.jsonl")); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "snapshot.meta.json"))
	if err != nil {
		return nil, err
	}
	var meta facts.SnapshotMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Request is one grade: which repository the pin anchors on and which member
// the comparison speaks for, the baseline, the policy, and what the caller
// declared about the change.
type Request struct {
	// Anchor is the repository whose output dir holds the pin: the first member.
	Anchor string
	// GradeRepo is the member the comparison speaks for; the anchor unless one
	// member of a union is graded against its slice of the pin.
	GradeRepo string
	// RepoPaths are every repository the current side holds, named when it
	// holds no facts.
	RepoPaths []string
	// Baseline selects the pin: pinned, previous, or a directory.
	Baseline string
	// Base is the baseline when the caller already holds it, as a replayed
	// member grade does; nil reads it from the anchor's baseline dir.
	Base   *facts.Snapshot
	Policy check.Policy
	// Focus narrows the delta to entries referencing a module, file or symbol.
	Focus string
	// Target and Expected declare the change's intended scope; either turns on
	// the conformance check and its spillover measurement.
	Target   string
	Expected []string
	// Reviewers reads authorship over ReviewerWindow commits for the modules the
	// change touched; nothing about authors is read without it.
	Reviewers      bool
	ReviewerWindow int
	Author         string
	// HistoryDir is the configured architecture-history directory, read for
	// dated rules.
	HistoryDir string
	// OtherSession, when set, says the baseline came from another agent session
	// still running on the repository; it joins the delta as a warning.
	OtherSession string
}

// Result is a grade's answer: the verdict, and the conformance report when the
// caller declared a scope.
type Result struct {
	Verdict     check.Verdict
	Conformance *conformance.Report
}

// BaselineError is a baseline that could not be read. The caller words the
// remedy, since the commands to suggest are named after its binary.
type BaselineError struct {
	Selector string
	Dir      string
	Err      error
}

func (e *BaselineError) Error() string {
	return fmt.Sprintf("could not load baseline from %q: %v", e.Dir, e.Err)
}

// Compute grades the current side eng publishes against the baseline.
func Compute(eng Engine, req Request, now time.Time) (Result, error) {
	snap := eng.Snapshot()
	if snap == nil || eng.Store().Count() == 0 {
		named := req.RepoPaths
		if len(named) == 0 {
			named = []string{req.GradeRepo}
		}
		return Result{}, fmt.Errorf("snapshot produced no facts for %s", strings.Join(named, ", "))
	}
	// Built from the store so it reflects the whole (possibly multi-repo) graph
	// rather than only the last repo indexed, FactsRef included: diff.Compute
	// reads its inputs and the published bundle is immutable, so copying the fact
	// set here would buy nothing. The meta is the graded member's: the pin
	// carries that member's provenance, so the current side is read for the same
	// member.
	current := &facts.Snapshot{Meta: eng.MetaFor(req.GradeRepo), Facts: eng.Store().FactsRef(), Insights: snap.Insights}
	base := req.Base
	if base == nil {
		baseDir := engine.ResolveBaselineDir(eng.OutputDir(req.Anchor), req.Baseline)
		var err error
		base, err = engine.LoadSnapshotDir(baseDir)
		if err != nil {
			return Result{}, &BaselineError{Selector: req.Baseline, Dir: baseDir, Err: err}
		}
	}

	// The committed suppression ledger joins the policy before grading. A ledger
	// that cannot be parsed is an operational failure, never a silent skip: half
	// a ledger would silence findings nobody signed off, or fail ones somebody did.
	policy := req.Policy
	suppressions, err := check.LoadSuppressions(req.GradeRepo)
	if err != nil {
		return Result{}, err
	}
	policy.Suppressions = suppressions

	d := diff.ComputeChanged(base, current, ChangedFiles(req.GradeRepo, base.Meta.Git, current.Meta.Git))
	if req.Focus != "" {
		d = d.Focused(req.Focus)
	}
	if req.OtherSession != "" {
		d.AddWarningKind(diff.WarnOtherSession, req.OtherSession)
	}

	// Conformance: did the change stay inside what the caller declared? Computed
	// only when something WAS declared: a gate that graded scope nobody stated
	// would be grading its own guess.
	// The baseline as a store, built once: conformance and the package-metrics
	// delta both score the pre-change graph.
	baseStore := facts.NewStore()
	baseStore.Add(base.Facts...)

	var measurements []check.Measurement
	var conf *conformance.Report
	if req.Target != "" || len(req.Expected) > 0 {
		rep := conformance.Compute(baseStore, eng.Store(), d, conformance.Options{
			Target:           req.Target,
			ExpectedPackages: req.Expected,
		})
		conf = &rep
		// Reported as a MEASUREMENT, not graded here. Whether spillover fails a
		// build is policy, and policy lives in one place.
		measurements = append(measurements, check.Measurement{
			Name:  "spillover_packages",
			Label: "package(s) reached outside the declared scope",
			Count: len(rep.Spillover),
		})
	}

	// EvaluateCurrent, not Evaluate: strict-mode constraint violations grade
	// against the CURRENT snapshot, baselined or not, the one deliberate
	// exception to delta scoping.
	verdict := check.EvaluateCurrent(d, policy, current.Insights, measurements...)
	blame := check.NewBlameReader(req.GradeRepo, eng.OutputDir(req.GradeRepo))
	verdict = check.ApplyTime(verdict, base, RevisionAt(req.GradeRepo, req.HistoryDir), blame.Age)
	if err := blame.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "enola: blame cache not written: %v\n", err)
	}
	verdict = check.RegradeIntersection(verdict, base, current, policy,
		check.OwnershipFromExtractors(eng.Extractors()), current.Insights, req.Focus, measurements...)
	verdict = check.AttachGuidance(verdict, eng.Store())
	verdict = check.AttachPackageMetrics(verdict, baseStore, eng.Store(), base, current,
		check.OwnershipFromExtractors(eng.Extractors()), req.Focus)
	// Opt-in, and gated here rather than inside AttachReviewers, so that without
	// the flag no git author name is read, computed, or printed at all.
	if req.Reviewers {
		verdict = check.AttachReviewers(verdict, eng.Store(),
			check.ReadAuthorship(req.GradeRepo, req.ReviewerWindow, eng.Store()),
			check.Actor(req.GradeRepo, req.Author))
	}
	verdict = check.AttachCensus(verdict, current.Meta, policy, current.Insights)
	verdict = check.AttachLedger(verdict, eng.Store(), policy, current.Insights, now)
	verdict = check.AttachSources(verdict, req.RepoPaths, eng.MetaFor)
	return Result{Verdict: verdict, Conformance: conf}, nil
}

// RevisionAt reads the architecture history for a dated rule: the newest
// revision at or before the date, reconstructed from its blob, and the first
// revision's date for a rule that predates the record.
func RevisionAt(repoPath, historyDir string) check.RevisionAt {
	return func(date time.Time) (*facts.Snapshot, string, string, bool) {
		root, err := pkghistory.Root(repoPath, historyDir)
		if err != nil {
			return nil, "", "", false
		}
		entries, err := pkghistory.Read(root)
		if err != nil || len(entries) == 0 {
			return nil, "", "", false
		}
		first := ""
		var chosen *pkghistory.Entry
		for i := range entries {
			e := entries[i]
			at, err := time.Parse(time.RFC3339, e.At)
			if err != nil || e.Blob == nil {
				continue
			}
			if first == "" || at.Format("2006-01-02") < first {
				first = at.Format("2006-01-02")
			}
			if !at.After(date.Add(24*time.Hour-time.Nanosecond)) && (chosen == nil || e.At > chosen.At) {
				chosen = &entries[i]
			}
		}
		if chosen == nil {
			return nil, "", first, false
		}
		snap, err := pkghistory.Load(root, *chosen)
		if err != nil {
			return nil, "", first, false
		}
		return snap, chosen.At[:10], first, true
	}
}

// ChangedFiles asks git which files the change touched between the two
// snapshots' commits, plus the working tree's own changes when the current
// snapshot was taken dirty. Nil when either side has no commit or git cannot
// answer, which hands the diff its fact-based fallback.
func ChangedFiles(repoPath string, base, current *facts.GitInfo) []string {
	if base == nil || current == nil || base.Commit == "" || current.Commit == "" {
		return nil
	}
	args := []string{"-C", repoPath, "diff", "--name-only", base.Commit, current.Commit}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	files := strings.Fields(string(out))
	if current.Dirty {
		if dirty, err := exec.Command("git", "-C", repoPath, "diff", "--name-only", "HEAD").Output(); err == nil {
			files = append(files, strings.Fields(string(dirty))...)
		}
		if untracked, err := exec.Command("git", "-C", repoPath, "ls-files", "--others", "--exclude-standard").Output(); err == nil {
			files = append(files, strings.Fields(string(untracked))...)
		}
	}
	if files == nil {
		files = []string{}
	}
	return files
}

// Render writes a grade as check prints it on standard output, in the format
// out names: the conformance report and the verdict as text, with the full
// delta under it when detail is asked, or the verdict in one of the machine
// formats, ending on one newline.
func Render(res Result, out check.Output, detail bool) (string, error) {
	if out.Format == check.FormatText {
		var sb strings.Builder
		if res.Conformance != nil {
			sb.WriteString(res.Conformance.Render())
		}
		sb.WriteString(res.Verdict.Render())
		if detail {
			fmt.Fprintf(&sb, "\n%s\n", res.Verdict.Detail())
		}
		return sb.String(), nil
	}
	written, err := res.Verdict.Write(out)
	if err != nil {
		return "", fmt.Errorf("failed to encode verdict: %v", err)
	}
	return strings.TrimRight(string(written), "\n") + "\n", nil
}

// ParseFailOn splits the explainer names a policy fails on into the ones that
// exist and the ones that do not, so the caller can refuse rather than enforce
// a policy it could not read. Matching is exact: `CYCLES` is not `cycles`,
// because a case-insensitive match would be a guess about which explainer was
// meant, and a policy exists to remove guesses about what fails. A list mixing
// good and bad names reports the bad ones rather than quietly enforcing the
// good half.
func ParseFailOn(names []string) (named, unknown []string) {
	for _, raw := range names {
		for _, part := range strings.Split(raw, ",") {
			n := strings.TrimSpace(part)
			if n == "" {
				continue
			}
			if slices.Contains(config.KnownExplainers, n) {
				named = append(named, n)
				continue
			}
			unknown = append(unknown, n)
		}
	}
	return named, unknown
}

// BaselineHelp turns a baseline that could not be read into a remedy rather
// than a bare stat error: the most common way a first grade fails is that no
// baseline was ever pinned. name is the command to suggest, and repoHint the
// repository the caller named, echoed into the suggested commands so they can
// be pasted as they are.
func BaselineHelp(name, selector, dir, repoHint string, err error) string {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "", "pinned":
		return fmt.Sprintf("no pinned baseline at %s\n\n"+
			"The gate needs a \"before\" to compare against, pinned BEFORE you edit:\n"+
			"    "+name+" baseline pin %s\n"+
			"    …make your change…\n"+
			"    "+name+" check %s\n\n"+
			"Or compare against the immediately-preceding snapshot with --baseline=previous.",
			dir, repoHint, repoHint)
	case "previous":
		return fmt.Sprintf("no previous snapshot at %s — that requires at least two snapshots "+
			"written with `"+name+" --generate` (note `"+name+" check` is read-only by default and does "+
			"not rotate it).\n\nFor a stable baseline across several rounds of edits, prefer:\n"+
			"    "+name+" baseline pin %s", dir, repoHint)
	default:
		return fmt.Sprintf("could not load baseline from %q: %v", dir, err)
	}
}
