package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/explainers/constraints"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

const BlastSampleCap = 20

const TargetPath = "path"

const TargetSymbol = "symbol"

var blastVias = []string{facts.RelCalls, facts.RelDependsOn, facts.RelImplements, facts.RelImports}

var memberKinds = []string{facts.KindModule, facts.KindSymbol, facts.KindRoute, facts.KindStorage}

type Generator interface {
	GenerateSnapshot(ctx context.Context, repoPath string, appendMode bool) (*facts.Snapshot, error)
}

type EngineFactory func() (Generator, error)

type Request struct {
	Paths   []string
	Symbols []string
	Patch   []byte
}

type Deps struct {
	RepoPath      string
	RepoLabel     string
	Store         *facts.Store
	FanIn         *FanIn
	Snapshot      SnapshotInfo
	OutputDirName string
	NewEngine     EngineFactory
	SkipRadius    bool
}

type SnapshotInfo struct {
	GeneratedAt string `json:"generated_at,omitempty"`
	Staleness   string `json:"staleness,omitempty"`
	Note        string `json:"note,omitempty"`
}

type Report struct {
	Repo                string          `json:"repo"`
	Snapshot            SnapshotInfo    `json:"snapshot"`
	ConstraintsDeclared bool            `json:"constraints_declared"`
	Targets             []TargetReport  `json:"targets"`
	Counterfactual      *Counterfactual `json:"counterfactual,omitempty"`
}

type TargetReport struct {
	Target        string                         `json:"target"`
	Kind          string                         `json:"kind"`
	Measured      bool                           `json:"measured"`
	Components    []constraints.ComponentBinding `json:"components,omitempty"`
	NoRuleGoverns bool                           `json:"no_rule_governs"`
	BlastRadius   *BlastRadius                   `json:"blast_radius,omitempty"`
	Radius        *constraints.BlastRadius       `json:"radius,omitempty"`
}

type BlastRadius struct {
	FanIn     int      `json:"fan_in"`
	FanOut    int      `json:"fan_out"`
	In        []string `json:"in,omitempty"`
	Out       []string `json:"out,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type Verdict struct {
	Rule     string           `json:"rule,omitempty"`
	Title    string           `json:"title"`
	Because  string           `json:"because,omitempty"`
	Evidence []facts.Evidence `json:"evidence,omitempty"`
}

type Counterfactual struct {
	PatchFiles          []string  `json:"patch_files"`
	ConstraintsDeclared bool      `json:"constraints_declared"`
	New                 []Verdict `json:"new"`
	Resolved            []Verdict `json:"resolved"`
	Unchanged           []Verdict `json:"unchanged"`
}

func ContractStore(repoPath string, measured []facts.Fact, cluster *intent.Declaration) (*facts.Store, error) {
	declared, err := Declared(repoPath, cluster)
	if err != nil {
		return nil, err
	}
	return StoreWith(measured, declared), nil
}

// Declared compiles the declaration in repoPath's working tree, under the
// cluster config's entry for it when there is one, into the facts the contract
// is read from. It reads the declaration files and nothing else, so a caller
// keeping a contract store can ask it on every call to learn whether the law
// moved.
func Declared(repoPath string, cluster *intent.Declaration) ([]facts.Fact, error) {
	fromFile, err := intent.LoadRepoFile(repoPath)
	if err != nil {
		return nil, fmt.Errorf("the working tree's declaration is invalid — fix it or run `enola constraints lint`: %w", err)
	}
	return intent.CompileFacts(intent.Resolve(fromFile, cluster)), nil
}

// StoreWith is the store a contract is read from: every measured fact except
// the intent the snapshot compiled, and the declared facts in its place.
func StoreWith(measured, declared []facts.Fact) *facts.Store {
	store := facts.NewStore()
	for _, f := range measured {
		if f.Kind == facts.KindIntent {
			continue
		}
		store.Add(f)
	}
	store.Add(declared...)
	return store
}

func Compute(ctx context.Context, req Request, deps Deps) (*Report, error) {
	if len(req.Paths) == 0 && len(req.Symbols) == 0 && len(req.Patch) == 0 {
		return nil, fmt.Errorf("nothing to plan: give --paths, --symbols, or --patch")
	}

	var patchFiles []PatchFile
	if len(req.Patch) > 0 {
		var err error
		patchFiles, err = ParsePatch(req.Patch)
		if err != nil {
			return nil, err
		}
		if err := ValidatePatchScope(patchFiles, deps.OutputDirName); err != nil {
			return nil, err
		}
	}

	report := &Report{Repo: deps.RepoLabel, Snapshot: deps.Snapshot}
	_, declared := constraints.ContractFor(deps.Store, "")
	report.ConstraintsDeclared = declared
	// A caller asking more than once keeps the index; otherwise it is built on
	// the first measured target, since a plan measuring nothing needs none.
	fanIn := func() *FanIn {
		if deps.FanIn == nil {
			deps.FanIn = IndexFanIn(deps.Store)
		}
		return deps.FanIn
	}
	for _, target := range pathTargets(req.Paths, patchFiles) {
		tr := targetReport(deps.Store, fanIn, deps.RepoLabel, target, TargetPath)
		if declared && tr.Measured && len(patchFiles) == 0 && !deps.SkipRadius {
			radius := constraints.BlastRadiusFor(deps.Store, []string{target})
			tr.Radius = &radius
		}
		report.Targets = append(report.Targets, tr)
	}
	for _, symbol := range sortedUnique(req.Symbols) {
		report.Targets = append(report.Targets, targetReport(deps.Store, fanIn, deps.RepoLabel, symbol, TargetSymbol))
	}

	if len(patchFiles) > 0 {
		if deps.NewEngine == nil {
			return nil, fmt.Errorf("the counterfactual needs an engine factory and none is wired")
		}
		cf, err := counterfactual(ctx, deps, patchFiles)
		if err != nil {
			return nil, err
		}
		report.Counterfactual = cf
	}
	return report, nil
}

func pathTargets(paths []string, patchFiles []PatchFile) []string {
	var all []string
	for _, p := range paths {
		all = append(all, filepath.ToSlash(p))
	}
	for _, f := range patchFiles {
		all = append(all, filepath.ToSlash(f.Path))
	}
	return sortedUnique(all)
}

func sortedUnique(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func targetReport(store *facts.Store, fanIn func() *FanIn, label, target, kind string) TargetReport {
	components, _ := constraints.ContractFor(store, target)
	tr := TargetReport{Target: target, Kind: kind, Components: components}
	tr.NoRuleGoverns = true
	for _, c := range components {
		if len(c.Rules) > 0 || len(c.Guidance) > 0 {
			tr.NoRuleGoverns = false
			break
		}
	}
	members := memberNamesFor(store, label, target, kind)
	tr.Measured = len(members) > 0
	if tr.Measured {
		tr.BlastRadius = blastRadius(store, fanIn(), label, target, kind, members)
	}
	return tr
}

func memberNamesFor(store *facts.Store, label, target, kind string) map[string]bool {
	names := map[string]bool{}
	if kind == TargetSymbol {
		for _, f := range store.LookupByExactName(target) {
			if isMemberKind(f.Kind) {
				names[f.Name] = true
			}
		}
		return names
	}
	for _, f := range inTargetFile(store, label, target) {
		if isMemberKind(f.Kind) {
			names[f.Name] = true
		}
	}
	return names
}

// inTargetFile is the facts whose file the path target names, read through the
// store's file index. A fact matches the target as written or under its
// repository's label, the plan's when it carries none, so those files are the
// only ones that can hold one. Reading every fact of a kind instead copied 2.1M
// dependency facts on each plan over a 2.3M-fact union to find one file's.
func inTargetFile(store *facts.Store, label, target string) []facts.Fact {
	files := []string{target, label + "/" + target}
	for _, repo := range store.RepoLabels() {
		files = append(files, repo+"/"+target)
	}
	var out []facts.Fact
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file] {
			continue
		}
		seen[file] = true
		for _, f := range store.ByFile(file) {
			if fileMatchesTarget(f, label, target) {
				out = append(out, f)
			}
		}
	}
	return out
}

func isMemberKind(kind string) bool {
	for _, mk := range memberKinds {
		if kind == mk {
			return true
		}
	}
	return false
}

func fileMatchesTarget(f facts.Fact, label, target string) bool {
	if f.File == "" {
		return false
	}
	if f.File == target {
		return true
	}
	prefix := f.Repo
	if prefix == "" {
		prefix = label
	}
	return strings.TrimPrefix(f.File, prefix+"/") == target
}

func blastRadius(store *facts.Store, fanIn *FanIn, label, target, kind string, members map[string]bool) *BlastRadius {
	viaSet := map[string]bool{}
	for _, via := range blastVias {
		viaSet[via] = true
	}

	out := map[string]bool{}
	collectOut := func(f facts.Fact) {
		for _, rel := range f.Relations {
			if viaSet[rel.Kind] && !members[rel.Target] {
				out[rel.Target] = true
			}
		}
	}
	for name := range members {
		for _, f := range store.LookupByExactName(name) {
			if isMemberKind(f.Kind) {
				collectOut(f)
			}
		}
	}
	if kind == TargetPath {
		for _, f := range inTargetFile(store, label, target) {
			if f.Kind == facts.KindDependency {
				collectOut(f)
			}
		}
	}

	in := fanIn.dependents(members)

	br := &BlastRadius{FanIn: len(in), FanOut: len(out)}
	br.In = capSorted(in, BlastSampleCap)
	br.Out = capSorted(out, BlastSampleCap)
	br.Truncated = len(br.In) < br.FanIn || len(br.Out) < br.FanOut
	return br
}

func capSorted(set map[string]bool, limit int) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > limit {
		names = names[:limit]
	}
	return names
}

func counterfactual(ctx context.Context, deps Deps, patchFiles []PatchFile) (*Counterfactual, error) {
	scratchRoot, err := os.MkdirTemp("", "enola-plan-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratchRoot) }()

	scratchRepo := filepath.Join(scratchRoot, filepath.Base(deps.RepoPath))
	skip := map[string]bool{}
	if deps.OutputDirName != "" {
		skip[filepath.ToSlash(deps.OutputDirName)] = true
	}
	if err := copyTree(deps.RepoPath, scratchRepo, skip); err != nil {
		return nil, fmt.Errorf("materializing the scratch copy: %w", err)
	}
	if err := applyPatch(scratchRepo, patchFiles); err != nil {
		return nil, err
	}

	baseline, err := generate(ctx, deps.NewEngine, deps.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("snapshotting the unpatched tree: %w", err)
	}
	patched, err := generate(ctx, deps.NewEngine, scratchRepo)
	if err != nil {
		return nil, fmt.Errorf("snapshotting the patched scratch copy: %w", err)
	}

	cf := &Counterfactual{
		ConstraintsDeclared: declaresConstraints(patched),
		New:                 []Verdict{},
		Resolved:            []Verdict{},
		Unchanged:           []Verdict{},
	}
	for _, f := range patchFiles {
		cf.PatchFiles = append(cf.PatchFiles, fmt.Sprintf("%s (%s)", f.Path, f.Op))
	}
	sort.Strings(cf.PatchFiles)

	before := constraintFindings(baseline.Insights)
	after := constraintFindings(patched.Insights)
	beforeTitles := map[string]bool{}
	for _, in := range before {
		beforeTitles[in.Title] = true
	}
	afterTitles := map[string]bool{}
	for _, in := range after {
		afterTitles[in.Title] = true
	}
	for _, in := range after {
		if beforeTitles[in.Title] {
			cf.Unchanged = append(cf.Unchanged, verdictOf(in))
		} else {
			cf.New = append(cf.New, verdictOf(in))
		}
	}
	for _, in := range before {
		if !afterTitles[in.Title] {
			cf.Resolved = append(cf.Resolved, verdictOf(in))
		}
	}
	sortVerdicts(cf.New)
	sortVerdicts(cf.Resolved)
	sortVerdicts(cf.Unchanged)
	return cf, nil
}

func generate(ctx context.Context, factory EngineFactory, repoPath string) (*facts.Snapshot, error) {
	eng, err := factory()
	if err != nil {
		return nil, err
	}
	return eng.GenerateSnapshot(ctx, repoPath, false)
}

func declaresConstraints(snap *facts.Snapshot) bool {
	for _, f := range snap.Facts {
		if f.Kind != facts.KindIntent {
			continue
		}
		if k, _ := f.PropAny("intent_kind").(string); k == "component" {
			return true
		}
	}
	return false
}

func constraintFindings(insights []facts.Insight) []facts.Insight {
	source := constraints.New().Name()
	var out []facts.Insight
	for _, in := range insights {
		if in.Source == source {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

func verdictOf(in facts.Insight) Verdict {
	return Verdict{
		Rule:     ruleOf(in.Title),
		Title:    in.Title,
		Because:  becauseOf(in.Description),
		Evidence: in.Evidence,
	}
}

func ruleOf(title string) string {
	for _, prefix := range []string{"Strict constraint ", "Advisory constraint ", "Constraint "} {
		if rest, ok := strings.CutPrefix(title, prefix); ok {
			if id, _, found := strings.Cut(rest, " violated:"); found {
				return id
			}
		}
	}
	if rest, ok := strings.CutPrefix(title, "Constraint rule "); ok {
		if id, _, found := strings.Cut(rest, " walked 0 edges"); found {
			return id
		}
	}
	for _, prefix := range []string{"forbid_reach rule ", "require_edge rule ", "protocol rule "} {
		if rest, ok := strings.CutPrefix(title, prefix); ok {
			if id, _, found := strings.Cut(rest, " skipped:"); found {
				return id
			}
		}
	}
	if rest, ok := strings.CutPrefix(title, "Guidance for "); ok {
		if _, id, found := strings.Cut(rest, ": "); found {
			return id
		}
	}
	return ""
}

func becauseOf(description string) string {
	i := strings.LastIndex(description, "Because: ")
	if i < 0 {
		return ""
	}
	return description[i+len("Because: "):]
}

func sortVerdicts(verdicts []Verdict) {
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].Title < verdicts[j].Title })
}
