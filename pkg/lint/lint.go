// Package lint reads a repository's declared constraint vocabulary the way an
// author needs it read: every validation problem with the file that declared
// it, and every declared component resolved against a snapshot, so what a
// selector selects is seen before a rule built on it verdicts anything.
//
// It writes the text a person reads and fills the document a program reads in
// one pass, so the two cannot disagree. It never generates a snapshot and never
// writes a file.
package lint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/enola-labs/enola/internal/explainers/constraints"
	"github.com/enola-labs/enola/internal/explainers/layers"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

// Member is one repository whose declaration is linted, with the cluster
// config's intent entry for it, when the config carries one.
type Member struct {
	Path    string
	Cluster *intent.Declaration
}

// Snapshot is what the declared components are resolved against: the facts of
// a snapshot, the directory it was read from and the label its facts carry.
// Loaded false means there is none, and lint validates without resolving.
type Snapshot struct {
	Dir    string
	Facts  []facts.Fact
	Label  string
	Loaded bool
}

// Run lints every member's declaration in order, then resolves the components
// they declare against snap, writing the text to w and returning the document
// the same pass filled. The last line of the text is the verdict. An error is
// a declaration that could not be read at all, which is the command not
// running rather than a problem it found.
func Run(w io.Writer, members []Member, snap Snapshot) (*Report, error) {
	l := newRun(w)
	problems := 0
	declared := facts.NewStore()
	for _, m := range members {
		n, err := l.repoDeclaration(m.Cluster, m.Path, declared)
		if err != nil {
			return nil, err
		}
		problems += n
	}
	problems += l.resolveComponents(snap, declared)
	l.report.Valid = problems == 0
	if problems > 0 {
		l.printf("\nFAIL — %s.\n", plural(problems, "validation problem"))
	} else {
		l.printf("\nOK — every declaration is valid.\n")
	}
	return l.report, nil
}

func (l *run) repoDeclaration(clusterDecl *intent.Declaration, repoPath string, declared *facts.Store) (int, error) {
	label := filepath.Base(repoPath)
	l.repo, l.repoPath = label, repoPath
	var unknown []intent.UnknownKey

	filePath := filepath.Join(repoPath, intent.RepoFileName)
	var fileDecl *intent.Declaration
	fileProblems := []string{}
	switch data, err := os.ReadFile(filePath); {
	case os.IsNotExist(err):
	case err != nil:
		return 0, fmt.Errorf("reading %s: %v", filePath, err)
	default:
		var d intent.Declaration
		if yamlErr := yaml.Unmarshal(data, &d); yamlErr != nil {
			fileProblems = append(fileProblems, fmt.Sprintf("not parseable as YAML: %v", yamlErr))
		} else {
			d.Normalize()
			d.Source = intent.RepoFileName
			fileDecl = &d
			unknown = append(unknown, intent.DeclarationUnknownKeys(data, intent.RepoFileName)...)
		}
	}

	dirFiles, dirProblems, err := intent.LoadConstraintsDir(repoPath)
	if err != nil {
		return 0, err
	}
	recipes, recipeFileProblems, err := intent.LoadRecipesDir(repoPath)
	if err != nil {
		return 0, err
	}
	for _, f := range dirFiles {
		unknown = append(unknown, f.UnknownKeys...)
	}
	for _, rec := range recipes {
		unknown = append(unknown, rec.UnknownKeys...)
	}
	recipes, builtinNotes := intent.MergeBuiltinRecipes(recipes)
	recipeProblems, recipeWarnings := intent.RecipeProblems(recipes)
	// A repository authoring a recipe the binary also ships is exercising the
	// override, not making a mistake, so the lint surface says which one ran
	// and nothing fails.
	recipeWarnings = append(recipeWarnings, builtinNotes...)
	recipeWarnings = append(recipeWarnings, intent.UnboundOptionalRules(recipes, dirFiles)...)

	hasFile := fileDecl != nil || len(fileProblems) > 0
	hasDir := len(dirFiles) > 0 || len(dirProblems) > 0
	hasRecipes := len(recipes) > 0 || len(recipeFileProblems) > 0
	switch {
	case !hasFile && !hasDir && !hasRecipes && clusterDecl == nil:
		l.printf("\n%s: no declaration (%s absent, no %s/ files, no cluster intent entry) — nothing to lint.\n", label, intent.RepoFileName, intent.ConstraintsDirName)
		return 0, nil
	case !hasFile && !hasDir && clusterDecl == nil:
		l.printf("\n%s: recipe definitions under %s/ only — nothing declares or instantiates them yet.\n", label, intent.RecipesDirName)
	case clusterDecl != nil && (hasFile || hasDir):
		l.printf("\n%s: cluster config overrides the repo's declaration wholesale; both are linted, the cluster entry governs.\n", label)
	case clusterDecl != nil:
		l.printf("\n%s: declared by the cluster config's intent entry.\n", label)
	case hasFile && hasDir:
		l.printf("\n%s: declared by %s plus %s under %s/.\n", label, intent.RepoFileName, plural(len(dirFiles), "constraints file"), intent.ConstraintsDirName)
	case hasDir:
		l.printf("\n%s: declared by constraints files under %s/.\n", label, intent.ConstraintsDirName)
	default:
		l.printf("\n%s: declared by %s.\n", label, intent.RepoFileName)
	}

	count := 0
	if fileDecl != nil {
		l.printf("  %s: %d component(s), %d rule(s), %d exemption(s)\n", filePath, len(fileDecl.Components), len(fileDecl.Rules), exemptionCount(fileDecl.Rules))
	}
	recipeByName := map[string]intent.Recipe{}
	for _, rec := range recipes {
		if _, seen := recipeByName[rec.Name]; !seen {
			recipeByName[rec.Name] = rec
		}
	}
	for _, f := range dirFiles {
		l.printf("  %s: %d component(s), %d rule(s), %d exemption(s)\n", f.Path, len(f.Components), len(f.Rules), exemptionCount(f.Rules))
		for _, inst := range f.UseRecipe {
			expanded := 0
			if rec, found := recipeByName[inst.Recipe]; found {
				expanded = len(rec.Rules)
			}
			binds := make([]string, 0, len(inst.Bind))
			for role := range inst.Bind {
				binds = append(binds, role)
			}
			sort.Strings(binds)
			bound := "nothing"
			if len(binds) > 0 {
				bound = strings.Join(binds, ", ")
			}
			l.printf("    use_recipe %s (recipe %s): binds %s, expands %s\n", inst.As, inst.Recipe, bound, plural(expanded, "rule"))
		}
	}
	for _, rec := range recipes {
		roles := make([]string, 0, len(rec.Roles))
		for _, role := range rec.Roles {
			roles = append(roles, role.Name)
		}
		l.printf("  %s: recipe %s — roles %s, %s\n", rec.Path, rec.Name, strings.Join(roles, ", "), plural(len(rec.Rules), "rule"))
	}
	report := func(source, file string, list []string) {
		for _, p := range list {
			l.printf("  %s: %s\n", source, p)
			l.problem(file, p)
			count++
		}
	}
	report(filePath, intent.RepoFileName, fileProblems)
	for _, p := range dirProblems {
		l.printf("  %s\n", p)
		l.problem("", p)
		count++
	}
	for _, p := range recipeFileProblems {
		l.printf("  %s\n", p)
		l.problem("", p)
		count++
	}
	for _, p := range recipeProblems {
		l.printf("  %s\n", p)
		l.problem("", p)
		count++
	}
	for _, w := range recipeWarnings {
		l.printf("  %s\n", w)
		l.report.Notes = append(l.report.Notes, w)
	}
	for _, u := range unknown {
		l.printf("  %s\n", u)
		l.report.UnknownKeys = append(l.report.UnknownKeys, UnknownKey{Repo: label, UnknownKey: u})
	}

	merged := intent.MergeConstraintsFiles(fileDecl, dirFiles)
	merged, expandProblems := intent.ApplyRecipes(merged, dirFiles, recipes)
	for _, p := range expandProblems {
		l.printf("  %s\n", p)
		l.problem("", p)
		count++
	}
	if merged != nil {
		for _, p := range merged.Problems() {
			if strings.HasPrefix(p, intent.ConstraintsDirName+"/") {
				l.printf("  %s\n", p)
				l.problem("", p)
			} else {
				l.printf("  %s: %s\n", filePath, p)
				l.problem(intent.RepoFileName, p)
			}
			count++
		}
	}
	if clusterDecl != nil {
		// Cluster entries were already validated when the config loaded — an
		// invalid one never reaches this command — so Problems here is a
		// belt-and-braces re-check that also keeps the report symmetric.
		l.printf("  cluster intent entry %s: %d component(s), %d rule(s), %d exemption(s)\n", label, len(clusterDecl.Components), len(clusterDecl.Rules), exemptionCount(clusterDecl.Rules))
		report("cluster intent entry "+label, "", clusterDecl.Problems())
	}

	resolved := intent.Resolve(merged, clusterDecl)
	if resolved != nil && len(resolved.Problems()) == 0 {
		declared.Add(intent.CompileFacts(resolved)...)
	}
	return count, nil
}

// pageCompiled reports whether an intent fact was compiled from a knowledge
// page rather than from a declaration file. Pages are the one intent carrier
// the working tree's declaration cannot reproduce, so these facts survive the
// swap that re-reads components and rules from disk.
func pageCompiled(f facts.Fact) bool {
	switch f.PropString("intent_kind") {
	case "page", "anchor", "relation":
		return true
	}
	return false
}

// StoreFor builds the store component resolution is reported against: every
// measured fact from the snapshot, the intent facts compiled from knowledge
// pages, and the DECLARATION read fresh from the working tree.
//
// The snapshot's own declaration facts are dropped so an edit to
// enola-intent.yaml or a constraints file is linted without regenerating.
// Page-compiled facts are not part of any declaration file and are kept: a
// component selecting by governed_by resolves through exactly those, and
// dropping them reported such a component as `0 member(s) — matches nothing;
// every rule naming it holds vacuously` while the explainer resolved its
// members and reported breaches against them. A lint that contradicts the
// verdict it exists to preview is worse than no lint.
//
// Declared facts are stamped with the snapshot's repo label before they join.
// The engine does the same thing (SetRepoRange over the extraction window),
// and a declared layer is resolved against the modules of the repo that OWNS
// it — so unlabelled intent facts beside labelled module facts resolve to
// nothing at all, which is precisely the silent-empty answer this report
// exists to expose.
func StoreFor(measured []facts.Fact, declared []facts.Fact, label string) *facts.Store {
	store := facts.NewStore()
	for _, f := range measured {
		if f.Kind == facts.KindIntent && !pageCompiled(f) {
			continue
		}
		store.Add(f)
	}
	if label != "" {
		for i := range declared {
			if declared[i].Repo == "" {
				declared[i].Repo = label
			}
		}
	}
	store.Add(declared...)
	return store
}

// resolveComponents joins the declared components against the snapshot on
// disk, if any: measured facts from the snapshot, component and rule facts
// from the declarations as they stand NOW — the file being edited, not the
// copy compiled into the snapshot — so the counts answer for the author's
// working tree. It returns the number of problems the join found. A selector
// naming a property the snapshot does not measure is one, because a predicate
// nothing can evaluate is a broken declaration rather than an empty component.
// It fails here, at authoring time, rather than being read as law from its own
// silence. A predicate component in an edge-walking role needs no snapshot to
// refuse: it is a defect of the declaration alone, and the validation pass
// above has already reported it.
func (l *run) resolveComponents(snap Snapshot, declared *facts.Store) int {
	if len(declared.ByKind(facts.KindIntent)) == 0 {
		l.report.Resolution = NothingDeclared
		return 0
	}
	outDir := snap.Dir
	if !snap.Loaded {
		l.printf("\nComponent resolution: no snapshot at %s - validation only.\n", outDir)
		l.report.Resolution = ValidationOnly
		return 0
	}
	l.report.Resolution = ResolvedAgainstSnapshot
	l.report.Snapshot = outDir
	store := StoreFor(snap.Facts, declared.ByKind(facts.KindIntent), snap.Label)

	unevaluableList := constraints.UnevaluableSelectors(store)
	unevaluable := map[string]bool{}
	for _, u := range unevaluableList {
		unevaluable[u.Component] = true
	}
	unasked := constraints.UnaskedComponents(store)

	l.printf("\nComponent resolution against the snapshot at %s:\n", outDir)
	for _, c := range constraints.MemberCounts(store) {
		note := ""
		state := ComponentResolved
		switch {
		case unasked[c.Component] != "":
			note = "  <- names service " + unasked[c.Component] + ", absent from this snapshot; unasked, never failed"
			state = ComponentUnasked
		case unevaluable[c.Component]:
			note = "  <- selector cannot be evaluated against this snapshot"
			state = ComponentUnevaluable
		case c.Members == 0:
			note = "  <- matches nothing; every rule naming it holds vacuously"
			state = ComponentMatchesNothing
		}
		l.report.Components = append(l.report.Components, Component{
			Name: c.Component, Selector: c.Selector, Members: c.Members, State: state, Service: unasked[c.Component],
		})
		l.printf("  %-24s %d member(s)%s\n", c.Component, c.Members, note)
		if c.Selector != "" {
			l.printf("  %-24s   %s\n", "", c.Selector)
		}
	}
	if len(unevaluableList) > 0 {
		l.printf("\nSelectors this snapshot cannot evaluate:\n")
		for _, u := range unevaluableList {
			suggestion := ""
			if len(u.NearMiss) > 0 {
				suggestion = fmt.Sprintf(" (measured properties with similar names: %s)", strings.Join(u.NearMiss, ", "))
			}
			l.printf("  %s: %s%s — declared in %s\n", u.Component, u.Problem(), suggestion, u.Source)
			l.problem(u.Source, u.Component+": "+u.Problem()+suggestion)
		}
		l.report.UnevaluableSelectors = unevaluableList
	}
	unreachable := constraints.UnreachableRoles(store)
	l.report.UnreachableRoles = unreachable
	if len(unreachable) > 0 {
		l.printf("\nRoles this snapshot resolves against nothing:\n")
		for _, u := range unreachable {
			verdict := "the rule emits no verdict"
			if u.Partial {
				verdict = "the rule's other edge kinds still verdict"
			}
			l.printf("  %s: %s — %s\n", u.Rule, u.Problem(), verdict)
		}
	}
	absent := constraints.AbsentExemplars(store)
	l.report.AbsentExemplars = absent
	if len(absent) > 0 {
		l.printf("\nGuidance exemplars the snapshot cannot resolve (a note, not an error):\n")
		for _, n := range absent {
			l.printf("  %s: %s\n", n.Rule, n.Exemplar)
		}
	}
	l.reportLayerResolution(store)
	return len(unevaluableList)
}

// reportLayerResolution prints what each declared layer selects, beside the
// component counts above it.
//
// Layers are resolved here for the same reason components are: a declaration is
// only worth what it selects, and the moment to learn that it selects nothing is
// while it is being written, not three snapshots later when a gate that was never
// going to fire keeps exiting 0. It is not counted as a lint PROBLEM — a layer
// order matching nothing is a mistake but not an invalid declaration, and the
// explainer already raises an advisory for it every snapshot.
func (l *run) reportLayerResolution(store *facts.Store) {
	counts := layers.MemberCounts(store)
	if len(counts) == 0 {
		return
	}
	l.report.Layers = counts
	l.printf("\nDeclared layer resolution (outermost first):\n")
	empty := 0
	lastRepo := ""
	for _, c := range counts {
		if c.Repo != lastRepo {
			if c.Repo != "" {
				l.printf("  %s:\n", c.Repo)
			}
			lastRepo = c.Repo
		}
		note := ""
		if c.Members == 0 {
			note = "  <- matches nothing; no import can violate this layer"
			empty++
		}
		l.printf("  %-24s %d module(s)%s\n", c.Layer, c.Members, note)
		l.printf("  %-24s   %s\n", "", strings.Join(c.Paths, " "))
	}
	if empty == 0 {
		return
	}
	// The measured module paths, once, at the end: a declared path and the path it
	// was meant to match are nearly always one visible edit apart, and without this
	// the author has to go query the fact store to see the second one.
	for _, repo := range dedupeRepos(counts) {
		names := layers.ModuleNames(store, repo)
		if len(names) == 0 {
			continue
		}
		if len(names) > 12 {
			names = append(names[:12:12], fmt.Sprintf("... and %d more", len(layers.ModuleNames(store, repo))-12))
		}
		label := "module paths this snapshot measured"
		if repo != "" {
			label = repo + ": " + label
		}
		l.printf("\n  %s:\n    %s\n", label, strings.Join(names, "\n    "))
	}
}

// dedupeRepos lists the repos named by a layer-count set, in first-seen order.
func dedupeRepos(counts []layers.LayerCount) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range counts {
		if seen[c.Repo] {
			continue
		}
		seen[c.Repo] = true
		out = append(out, c.Repo)
	}
	return out
}

// SnapshotLabel names the repo label the snapshot's facts carry. The recorded
// label wins; snapshots written before RepoLabel existed fall back to the label
// their own facts were tagged with, which is what those builds used.
func SnapshotLabel(snap *facts.Snapshot) string {
	if snap == nil {
		return ""
	}
	if snap.Meta.RepoLabel != "" {
		return snap.Meta.RepoLabel
	}
	for _, f := range snap.Facts {
		if f.Repo != "" {
			return f.Repo
		}
	}
	return ""
}

func exemptionCount(rules []intent.ConstraintRule) int {
	n := 0
	for _, rule := range rules {
		n += len(rule.Exempt)
	}
	return n
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
